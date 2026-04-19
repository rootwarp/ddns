// Package resolver queries the configured HTTPS echo services in parallel
// and returns the quorum IPv4 address.
//
// Design notes:
//
//   - Every configured source is queried concurrently under a per-request
//     timeout derived from ResolverConfig.Timeout. The first failure does
//     not cancel the other sources — we want every source to run to
//     completion or timeout so we maximize the chance of reaching quorum.
//   - Per-source response bodies are bounded to 64 bytes via io.LimitReader
//     to guard against a misbehaving service streaming megabytes at us.
//   - Per-source responses are additionally sanity-rejected if they fall
//     inside any RFC 1918 / loopback / link-local / CGNAT / multicast /
//     benchmark prefix (see sanitize.go). The rejected source's Error
//     becomes sanitize_reject:in_<prefix> and the address is not counted
//     toward quorum.
//   - The tally picks the IP with the most agreeing sources; if that count
//     is below cfg.Quorum, Resolve returns ddnserr.ErrNoQuorum along with
//     the full ResolveReport so callers can log per-source outcomes.
package resolver

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/rootwarp/ddns/internal/config"
	"github.com/rootwarp/ddns/internal/ddnserr"
)

// IPResolver is the seam callers should program against. Phase 2.6 types the
// daemon field as IPResolver so tests can substitute a fake without an
// interface refactor later.
type IPResolver interface {
	Resolve(ctx context.Context) (netip.Addr, ResolveReport, error)
}

// ResolveReport is the structured per-source outcome of one Resolve call.
type ResolveReport struct {
	Sources   []SourceResult
	Quorum    int   // agreement count for the returned IP; meaningful even on ErrNoQuorum
	ElapsedMs int64 // wall-clock duration of the fan-out in ms
}

// SourceResult captures one source's outcome.
type SourceResult struct {
	URL       string
	IP        netip.Addr // zero-value when Error is non-empty
	Error     string     // short machine-readable reason ("parse_err", "not_ipv4", "http_503", or a transport error)
	ElapsedMs int64
}

// Resolver is the concrete IPResolver that fans out HTTP GETs to the
// configured echo services.
type Resolver struct {
	sources []string
	quorum  int
	timeout time.Duration
	client  *http.Client
}

// Compile-time assertion that *Resolver satisfies IPResolver.
var _ IPResolver = (*Resolver)(nil)

// maxBodyBytes caps the per-source read. A valid IPv4 plus trailing whitespace
// fits easily under 64 bytes; anything longer is noise we can discard.
const maxBodyBytes = 64

// New constructs a *Resolver with the fixed-client tuning called out in the
// architecture doc: 5s TLS handshake timeout, keep-alives disabled (each
// tick's small request volume does not benefit from reuse, and residential
// NATs sometimes produce dead-socket ambiguity on reused connections).
func New(cfg config.ResolverConfig) *Resolver {
	return &Resolver{
		sources: append([]string(nil), cfg.Sources...),
		quorum:  cfg.Quorum,
		timeout: cfg.Timeout,
		client: &http.Client{
			Transport: &http.Transport{
				TLSHandshakeTimeout: 5 * time.Second,
				DisableKeepAlives:   true,
			},
		},
	}
}

// Resolve fans out one GET per configured source under a per-request
// timeout, parses each response, and tallies the per-IP agreement counts.
// It returns the highest-agreement IP; if that count is below the
// configured quorum, it returns ddnserr.ErrNoQuorum and the zero netip.Addr.
// The full ResolveReport is returned in both cases for observability.
func (r *Resolver) Resolve(ctx context.Context) (netip.Addr, ResolveReport, error) {
	started := time.Now()

	// Empty source list: belt+braces guard (config.Load rejects this).
	if len(r.sources) == 0 {
		return netip.Addr{}, ResolveReport{
			ElapsedMs: time.Since(started).Milliseconds(),
		}, fmt.Errorf("resolver: %w: no sources configured", ddnserr.ErrNoQuorum)
	}

	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	results := make([]SourceResult, len(r.sources))
	var wg sync.WaitGroup
	var mu sync.Mutex

	for i, src := range r.sources {
		wg.Add(1)
		go func(idx int, url string) {
			defer wg.Done()
			res := r.fetchOne(ctx, url)
			mu.Lock()
			results[idx] = res
			mu.Unlock()
		}(i, src)
	}
	wg.Wait()

	// Tally by IP string to avoid netip.Addr-as-map-key concerns.
	counts := make(map[netip.Addr]int)
	for _, res := range results {
		if res.Error != "" {
			continue
		}
		counts[res.IP]++
	}

	var bestIP netip.Addr
	var bestCount int
	for ip, c := range counts {
		if c > bestCount {
			bestIP = ip
			bestCount = c
		}
	}

	report := ResolveReport{
		Sources:   results,
		Quorum:    bestCount,
		ElapsedMs: time.Since(started).Milliseconds(),
	}

	if bestCount < r.quorum || !bestIP.IsValid() {
		return netip.Addr{}, report, fmt.Errorf("resolver: %w", ddnserr.ErrNoQuorum)
	}
	return bestIP, report, nil
}

// fetchOne performs one HTTP GET and returns the parsed SourceResult.
// Errors become short machine-readable reasons so the aggregated
// ResolveReport is easy to grep in logs.
func (r *Resolver) fetchOne(ctx context.Context, url string) SourceResult {
	started := time.Now()
	result := SourceResult{URL: url}
	defer func() {
		result.ElapsedMs = time.Since(started).Milliseconds()
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		result.Error = fmt.Sprintf("request_err: %v", err)
		result.ElapsedMs = time.Since(started).Milliseconds()
		return result
	}
	resp, err := r.client.Do(req)
	if err != nil {
		result.Error = fmt.Sprintf("transport_err: %v", err)
		result.ElapsedMs = time.Since(started).Milliseconds()
		return result
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		result.Error = fmt.Sprintf("http_%d", resp.StatusCode)
		result.ElapsedMs = time.Since(started).Milliseconds()
		return result
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		result.Error = fmt.Sprintf("read_err: %v", err)
		result.ElapsedMs = time.Since(started).Milliseconds()
		return result
	}

	body := strings.TrimSpace(string(raw))
	addr, err := netip.ParseAddr(body)
	if err != nil {
		result.Error = fmt.Sprintf("parse_err: %v", err)
		result.ElapsedMs = time.Since(started).Milliseconds()
		return result
	}
	if !addr.Is4() {
		result.Error = fmt.Sprintf("not_ipv4: %s", addr.String())
		result.ElapsedMs = time.Since(started).Milliseconds()
		return result
	}

	// Sanity-reject private/loopback/CGNAT/link-local/multicast/benchmark
	// addresses. A successful HTTP response containing such an address is
	// never a legitimate public IP for this daemon; the most likely cause
	// is an echo service misbehaving or being proxied by a middlebox that
	// stripped the real client address. See internal/resolver/sanitize.go
	// for the full prefix list.
	if ok, reason := sanitize(addr); !ok {
		result.Error = "sanitize_reject:" + reason
		result.ElapsedMs = time.Since(started).Milliseconds()
		return result
	}

	result.IP = addr
	result.ElapsedMs = time.Since(started).Milliseconds()
	return result
}
