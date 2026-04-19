// Package gcp implements dnsprovider.DNSProvider against Google Cloud DNS
// via google.golang.org/api/dns/v1.
//
// Design notes:
//
//   - `New(ctx)` is a thin wrapper around `dns.NewService` that requests only
//     the readwrite scope the daemon needs. Any failure there is classified
//     as ErrAuth — the only way dns.NewService fails at construction time is
//     when Application Default Credentials cannot be discovered or parsed.
//   - `Get` translates the googleapi error code into our sentinel taxonomy:
//     404 → ErrNotFound (terminal, used by the daemon to pick the create
//     path), 403 → ErrAuth, 429/5xx → ErrTransient. Other errors are also
//     wrapped as ErrTransient with a short prefix so the next tick retries.
//   - `Upsert` does a Get-then-Changes.Create sequence: the preceding Get
//     produces the precise `deletions` payload Cloud DNS requires for an
//     atomic RRset swap (spike 01 §Q3). An internal no-op safeguard short-
//     circuits without a write when the live record already matches — the
//     daemon also checks, but the provider boundary keeps the safeguard.
//   - `newWithService` is the package-private seam tests use to inject a
//     *dns.Service built around a mock http.Client. It's deliberately not
//     exported: the public API is `New(ctx)` only.
package gcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"sort"

	"google.golang.org/api/dns/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/rootwarp/ddns/internal/ddnserr"
	"github.com/rootwarp/ddns/internal/dnsprovider"
)

// Provider is the Google Cloud DNS implementation of dnsprovider.DNSProvider.
type Provider struct {
	svc *dns.Service
}

// Compile-time assertion that *Provider satisfies dnsprovider.DNSProvider.
var _ dnsprovider.DNSProvider = (*Provider)(nil)

// New constructs a Provider whose underlying *dns.Service uses Application
// Default Credentials scoped to Cloud DNS readwrite. Any error here maps to
// ErrAuth — at this layer, there's no other meaningful failure mode.
func New(ctx context.Context) (*Provider, error) {
	svc, err := dns.NewService(ctx, option.WithScopes(dns.NdevClouddnsReadwriteScope))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ddnserr.ErrAuth, err)
	}
	return &Provider{svc: svc}, nil
}

// newWithService is the test-only constructor. It is package-private so the
// injection seam does not leak into the public API. Tests use it with a
// *dns.Service built from option.WithHTTPClient(mockClient).
func newWithService(svc *dns.Service) *Provider {
	return &Provider{svc: svc}
}

// Get fetches the current RRset for ref. A 404 is translated to
// ddnserr.ErrNotFound (terminal sentinel — do not wrap). All other errors
// are wrapped with a status-appropriate sentinel.
func (p *Provider) Get(ctx context.Context, ref dnsprovider.RecordRef) (dnsprovider.Record, error) {
	resp, err := p.svc.ResourceRecordSets.
		Get(ref.Project, ref.ManagedZone, ref.Name, ref.Type).
		Context(ctx).
		Do()
	if err != nil {
		return dnsprovider.Record{}, classifyGetError(err)
	}
	return dnsprovider.Record{
		Rrdatas: resp.Rrdatas,
		TTL:     resp.Ttl,
	}, nil
}

// Upsert replaces the RRset atomically: a single Changes.Create carries the
// precise `deletions` payload observed by an internal Get, plus the
// `additions` payload built from new. Status "done" and "pending" both count
// as success (spike 01 §Q2). On a live record already matching new, no API
// write is issued.
func (p *Provider) Upsert(ctx context.Context, ref dnsprovider.RecordRef, newRec dnsprovider.Record) (dnsprovider.UpsertResult, error) {
	if err := assertUpsertInvariants(ref, newRec); err != nil {
		return dnsprovider.UpsertResult{}, err
	}

	old, err := p.Get(ctx, ref)
	havePrior := true
	if err != nil {
		if errors.Is(err, ddnserr.ErrNotFound) {
			havePrior = false
		} else {
			return dnsprovider.UpsertResult{}, err
		}
	}

	// No-op safeguard: if the live record matches byte-for-byte (order-
	// independent for rrdatas — Cloud DNS does not promise order on Get)
	// and the TTL is already what we want, skip the write entirely.
	if havePrior && sameRrdatas(old.Rrdatas, newRec.Rrdatas) && old.TTL == newRec.TTL {
		return dnsprovider.UpsertResult{
			Changed:    false,
			OldRrdatas: append([]string(nil), old.Rrdatas...),
			NewRrdatas: append([]string(nil), old.Rrdatas...),
		}, nil
	}

	change := &dns.Change{
		Additions: []*dns.ResourceRecordSet{{
			Name:    ref.Name,
			Type:    ref.Type,
			Ttl:     newRec.TTL,
			Rrdatas: append([]string(nil), newRec.Rrdatas...),
		}},
	}
	if havePrior {
		change.Deletions = []*dns.ResourceRecordSet{{
			Name:    ref.Name,
			Type:    ref.Type,
			Ttl:     old.TTL,
			Rrdatas: append([]string(nil), old.Rrdatas...),
		}}
	}

	if _, err := p.svc.Changes.
		Create(ref.Project, ref.ManagedZone, change).
		Context(ctx).
		Do(); err != nil {
		return dnsprovider.UpsertResult{}, classifyUpsertError(err)
	}

	var oldRrdatas []string
	if havePrior {
		oldRrdatas = append([]string(nil), old.Rrdatas...)
	}
	return dnsprovider.UpsertResult{
		Changed:    true,
		OldRrdatas: oldRrdatas,
		NewRrdatas: append([]string(nil), newRec.Rrdatas...),
	}, nil
}

// classifyGetError maps a *googleapi.Error to a sentinel. 404 is terminal
// and returned verbatim so callers can check errors.Is without interference.
func classifyGetError(err error) error {
	var gErr *googleapi.Error
	if errors.As(err, &gErr) {
		switch gErr.Code {
		case http.StatusNotFound:
			return ddnserr.ErrNotFound
		case http.StatusForbidden, http.StatusUnauthorized:
			return fmt.Errorf("%w: provider get: %v", ddnserr.ErrAuth, err)
		case http.StatusTooManyRequests,
			http.StatusInternalServerError,
			http.StatusBadGateway,
			http.StatusServiceUnavailable,
			http.StatusGatewayTimeout:
			return fmt.Errorf("%w: provider get: %v", ddnserr.ErrTransient, err)
		}
	}
	return fmt.Errorf("%w: provider get: %v", ddnserr.ErrTransient, err)
}

// classifyUpsertError maps a *googleapi.Error from Changes.Create to a
// sentinel. 412/409 are transient (precondition race — next tick re-reads).
func classifyUpsertError(err error) error {
	var gErr *googleapi.Error
	if errors.As(err, &gErr) {
		switch gErr.Code {
		case http.StatusNotFound:
			return ddnserr.ErrNotFound
		case http.StatusForbidden, http.StatusUnauthorized:
			return fmt.Errorf("%w: provider upsert: %v", ddnserr.ErrAuth, err)
		case http.StatusPreconditionFailed,
			http.StatusConflict,
			http.StatusTooManyRequests,
			http.StatusInternalServerError,
			http.StatusBadGateway,
			http.StatusServiceUnavailable,
			http.StatusGatewayTimeout:
			return fmt.Errorf("%w: provider upsert: %v", ddnserr.ErrTransient, err)
		}
	}
	return fmt.Errorf("%w: provider upsert: %v", ddnserr.ErrTransient, err)
}

// assertUpsertInvariants enforces the v1 A-record contract. These are
// programmer errors: a misbehaving caller should see a loud "ddns bug:"
// message rather than a silent coerce or an ErrTransient masquerade.
func assertUpsertInvariants(ref dnsprovider.RecordRef, newRec dnsprovider.Record) error {
	if ref.Type != "A" {
		return fmt.Errorf("ddns bug: provider only supports Type=A in v1, got %q", ref.Type)
	}
	if len(newRec.Rrdatas) == 0 {
		return fmt.Errorf("ddns bug: Upsert called with empty Rrdatas")
	}
	for _, v := range newRec.Rrdatas {
		addr, err := netip.ParseAddr(v)
		if err != nil || !addr.Is4() {
			return fmt.Errorf("ddns bug: Upsert Rrdata %q is not a valid IPv4 address", v)
		}
	}
	return nil
}

// sameRrdatas returns true when a and b contain the same set of entries.
// Cloud DNS does not promise ordering on Get; we sort before comparing so a
// live "198.51.100.7,192.0.2.1" is equivalent to a desired
// "192.0.2.1,198.51.100.7".
func sameRrdatas(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aa := append([]string(nil), a...)
	bb := append([]string(nil), b...)
	sort.Strings(aa)
	sort.Strings(bb)
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}
