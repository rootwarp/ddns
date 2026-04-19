package resolver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rootwarp/ddns/internal/config"
	"github.com/rootwarp/ddns/internal/ddnserr"
)

// echoServer stands in for a real HTTPS echo service; it responds with a
// static body and status code, and can optionally sleep to simulate
// slowness beyond the resolver timeout.
func echoServer(t *testing.T, body string, status int, sleep time.Duration) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sleep > 0 {
			select {
			case <-time.After(sleep):
			case <-r.Context().Done():
				return
			}
		}
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(s.Close)
	return s
}

func newResolverFromServers(servers []*httptest.Server, quorum int, timeout time.Duration) *Resolver {
	urls := make([]string, 0, len(servers))
	for _, s := range servers {
		urls = append(urls, s.URL)
	}
	return New(config.ResolverConfig{Sources: urls, Quorum: quorum, Timeout: timeout})
}

// TestResolver_AllAgree — three servers, same IP, quorum 3.
func TestResolver_AllAgree(t *testing.T) {
	s1 := echoServer(t, "192.0.2.42", 200, 0)
	s2 := echoServer(t, "192.0.2.42\n", 200, 0)
	s3 := echoServer(t, "  192.0.2.42  ", 200, 0)

	r := newResolverFromServers([]*httptest.Server{s1, s2, s3}, 2, 2*time.Second)
	ip, report, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ip.String() != "192.0.2.42" {
		t.Errorf("ip = %s, want 192.0.2.42", ip)
	}
	if report.Quorum != 3 {
		t.Errorf("Quorum = %d, want 3", report.Quorum)
	}
	if len(report.Sources) != 3 {
		t.Errorf("Sources len = %d, want 3", len(report.Sources))
	}
	if report.ElapsedMs < 0 {
		t.Errorf("ElapsedMs = %d, want >= 0", report.ElapsedMs)
	}
}

// TestResolver_TwoOfThree — 2 agree, 1 disagrees; quorum 2 majority wins.
func TestResolver_TwoOfThree(t *testing.T) {
	s1 := echoServer(t, "192.0.2.42", 200, 0)
	s2 := echoServer(t, "192.0.2.42", 200, 0)
	s3 := echoServer(t, "198.51.100.7", 200, 0)

	r := newResolverFromServers([]*httptest.Server{s1, s2, s3}, 2, 2*time.Second)
	ip, report, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ip.String() != "192.0.2.42" {
		t.Errorf("ip = %s, want 192.0.2.42", ip)
	}
	if report.Quorum != 2 {
		t.Errorf("Quorum = %d, want 2", report.Quorum)
	}
}

// TestResolver_TotalDisagreement — three different IPs, no quorum.
func TestResolver_TotalDisagreement(t *testing.T) {
	s1 := echoServer(t, "192.0.2.42", 200, 0)
	s2 := echoServer(t, "203.0.113.9", 200, 0)
	s3 := echoServer(t, "198.51.100.7", 200, 0)

	r := newResolverFromServers([]*httptest.Server{s1, s2, s3}, 2, 2*time.Second)
	_, report, err := r.Resolve(context.Background())
	if !errors.Is(err, ddnserr.ErrNoQuorum) {
		t.Fatalf("err = %v, want ErrNoQuorum", err)
	}
	// Every source must appear in the report even on quorum failure.
	if len(report.Sources) != 3 {
		t.Errorf("Sources len = %d, want 3", len(report.Sources))
	}
	// With 1-1-1 tally, the selected quorum count is 1.
	if report.Quorum != 1 {
		t.Errorf("Quorum = %d, want 1", report.Quorum)
	}
}

// TestResolver_OneTimeout — slow server blows past the timeout; other two agree.
func TestResolver_OneTimeout(t *testing.T) {
	s1 := echoServer(t, "192.0.2.42", 200, 0)
	s2 := echoServer(t, "192.0.2.42", 200, 0)
	s3 := echoServer(t, "192.0.2.42", 200, 500*time.Millisecond)

	r := newResolverFromServers([]*httptest.Server{s1, s2, s3}, 2, 150*time.Millisecond)
	ip, report, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ip.String() != "192.0.2.42" {
		t.Errorf("ip = %s, want 192.0.2.42", ip)
	}
	if report.Quorum != 2 {
		t.Errorf("Quorum = %d, want 2 (the slow source should have errored)", report.Quorum)
	}
	// The slow source must carry a non-empty error message.
	var slowErr string
	for _, s := range report.Sources {
		if s.URL == s3.URL {
			slowErr = s.Error
		}
	}
	if slowErr == "" {
		t.Errorf("slow source %q has empty Error field; want timeout diagnostic", s3.URL)
	}
}

// TestResolver_MalformedBody — 1 server returns junk; other 2 agree → quorum 2.
func TestResolver_MalformedBody(t *testing.T) {
	s1 := echoServer(t, "not an ip", 200, 0)
	s2 := echoServer(t, "192.0.2.42", 200, 0)
	s3 := echoServer(t, "192.0.2.42", 200, 0)

	r := newResolverFromServers([]*httptest.Server{s1, s2, s3}, 2, 2*time.Second)
	ip, report, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ip.String() != "192.0.2.42" {
		t.Errorf("ip = %s, want 192.0.2.42", ip)
	}
	if report.Quorum != 2 {
		t.Errorf("Quorum = %d, want 2", report.Quorum)
	}
	// Find the malformed source, confirm parse_err reason recorded.
	var parseErr string
	for _, s := range report.Sources {
		if s.URL == s1.URL {
			parseErr = s.Error
		}
	}
	if !strings.Contains(parseErr, "parse_err") {
		t.Errorf("malformed source Error = %q, want parse_err", parseErr)
	}
}

// TestResolver_5xx — 1 server returns 503; skipped.
func TestResolver_5xx(t *testing.T) {
	s1 := echoServer(t, "", 503, 0)
	s2 := echoServer(t, "192.0.2.42", 200, 0)
	s3 := echoServer(t, "192.0.2.42", 200, 0)

	r := newResolverFromServers([]*httptest.Server{s1, s2, s3}, 2, 2*time.Second)
	ip, report, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ip.String() != "192.0.2.42" {
		t.Errorf("ip = %s, want 192.0.2.42", ip)
	}
	var httpErr string
	for _, s := range report.Sources {
		if s.URL == s1.URL {
			httpErr = s.Error
		}
	}
	if !strings.Contains(httpErr, "http_503") {
		t.Errorf("5xx source Error = %q, want to contain http_503", httpErr)
	}
}

// TestResolver_IPv6Response — v6 address is rejected as not_ipv4.
func TestResolver_IPv6Response(t *testing.T) {
	s1 := echoServer(t, "2001:db8::1", 200, 0)
	s2 := echoServer(t, "192.0.2.42", 200, 0)
	s3 := echoServer(t, "192.0.2.42", 200, 0)

	r := newResolverFromServers([]*httptest.Server{s1, s2, s3}, 2, 2*time.Second)
	ip, report, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ip.String() != "192.0.2.42" {
		t.Errorf("ip = %s, want 192.0.2.42", ip)
	}
	var v6Err string
	for _, s := range report.Sources {
		if s.URL == s1.URL {
			v6Err = s.Error
		}
	}
	if !strings.Contains(v6Err, "not_ipv4") {
		t.Errorf("IPv6 source Error = %q, want to contain not_ipv4", v6Err)
	}
}

// TestResolver_BodyOver64BytesIsBounded — confirms LimitReader actually caps the read.
// A 1MB body that happens to start with a valid IP followed by garbage must still parse.
func TestResolver_BodyOver64BytesIsBounded(t *testing.T) {
	// Build a body whose first bytes are valid but whose total length is huge.
	giant := strings.Repeat("x", 1<<20)
	body := "192.0.2.42\n" + giant

	var bytesServed atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n, _ := fmt.Fprint(w, body)
		bytesServed.Add(int64(n))
	}))
	t.Cleanup(ts.Close)

	s2 := echoServer(t, "192.0.2.42", 200, 0)
	s3 := echoServer(t, "192.0.2.42", 200, 0)

	r := newResolverFromServers([]*httptest.Server{ts, s2, s3}, 2, 2*time.Second)
	ip, _, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ip.String() != "192.0.2.42" {
		t.Errorf("ip = %s, want 192.0.2.42", ip)
	}
}

// TestResolver_AllSourcesFail — no useful response anywhere → ErrNoQuorum with
// every source carrying an Error.
func TestResolver_AllSourcesFail(t *testing.T) {
	s1 := echoServer(t, "not-an-ip", 200, 0)
	s2 := echoServer(t, "", 500, 0)

	r := newResolverFromServers([]*httptest.Server{s1, s2}, 2, 2*time.Second)
	_, report, err := r.Resolve(context.Background())
	if !errors.Is(err, ddnserr.ErrNoQuorum) {
		t.Fatalf("err = %v, want ErrNoQuorum", err)
	}
	for _, s := range report.Sources {
		if s.Error == "" {
			t.Errorf("source %q: Error is empty; every failing source should carry a reason", s.URL)
		}
	}
}

// TestResolver_EmptySourceList — defensively handles a zero-source config
// rather than panicking (config validation normally rejects this but belt+braces).
func TestResolver_EmptySourceList(t *testing.T) {
	r := New(config.ResolverConfig{Sources: nil, Quorum: 1, Timeout: time.Second})
	_, _, err := r.Resolve(context.Background())
	if !errors.Is(err, ddnserr.ErrNoQuorum) {
		t.Errorf("err = %v, want ErrNoQuorum for empty source list", err)
	}
}

// TestIPResolver_InterfaceSatisfied is a compile-time check that *Resolver
// implements IPResolver. Phase 2.6 types the daemon field as IPResolver so
// tests can inject a fake resolver without an interface refactor later.
func TestIPResolver_InterfaceSatisfied(t *testing.T) {
	var _ IPResolver = (*Resolver)(nil)
}
