package resolver

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/rootwarp/ddns/internal/ddnserr"
)

// Issue 4.3: sanity-reject tests.
//
// Each test spins three echoServers: one returns a rejected address, the
// others agree on a valid public IP. Quorum is configured at 2, so the
// rejection must drop the bad source while the other two still reach
// quorum. The rejected source's Error field is asserted for the
// "sanitize_reject:in_<prefix>" shape.

func findSource(sources []SourceResult, url string) SourceResult {
	for _, s := range sources {
		if s.URL == url {
			return s
		}
	}
	return SourceResult{}
}

func runSanitizeCase(t *testing.T, badBody, wantReasonFragment string) {
	t.Helper()
	bad := echoServer(t, badBody, 200, 0)
	good1 := echoServer(t, "192.0.2.42", 200, 0)
	good2 := echoServer(t, "192.0.2.42", 200, 0)

	r := newResolverFromServers([]*httptest.Server{bad, good1, good2}, 2, 2*time.Second)
	ip, report, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ip.String() != "192.0.2.42" {
		t.Fatalf("ip = %s, want 192.0.2.42", ip)
	}
	if report.Quorum != 2 {
		t.Fatalf("Quorum = %d, want 2 (bad source rejected, other two agree)", report.Quorum)
	}
	badRes := findSource(report.Sources, bad.URL)
	if !strings.HasPrefix(badRes.Error, "sanitize_reject:") {
		t.Fatalf("bad source Error = %q, want sanitize_reject: prefix", badRes.Error)
	}
	if !strings.Contains(badRes.Error, wantReasonFragment) {
		t.Fatalf("bad source Error = %q, want to contain %q", badRes.Error, wantReasonFragment)
	}
}

func TestResolver_Reject_Private_192_168(t *testing.T) {
	runSanitizeCase(t, "192.168.1.1", "192.168.0.0/16")
}

func TestResolver_Reject_Private_10(t *testing.T) {
	runSanitizeCase(t, "10.0.0.5", "10.0.0.0/8")
}

func TestResolver_Reject_Private_172_16(t *testing.T) {
	runSanitizeCase(t, "172.16.1.1", "172.16.0.0/12")
}

func TestResolver_Reject_Loopback_127(t *testing.T) {
	runSanitizeCase(t, "127.0.0.1", "127.0.0.0/8")
}

func TestResolver_Reject_CGNAT_100_64(t *testing.T) {
	runSanitizeCase(t, "100.64.0.5", "100.64.0.0/10")
}

func TestResolver_Reject_LinkLocal_169_254(t *testing.T) {
	runSanitizeCase(t, "169.254.1.1", "169.254.0.0/16")
}

func TestResolver_Reject_Multicast_224(t *testing.T) {
	runSanitizeCase(t, "224.0.0.1", "224.0.0.0/4")
}

func TestResolver_Reject_Benchmark_198_18(t *testing.T) {
	runSanitizeCase(t, "198.18.1.1", "198.18.0.0/15")
}

// TestResolver_AllSourcesRejected_NoQuorum — three sources all return
// private addresses → ErrNoQuorum with all three reported as
// sanitize_reject.
func TestResolver_AllSourcesRejected_NoQuorum(t *testing.T) {
	s1 := echoServer(t, "192.168.1.1", 200, 0)
	s2 := echoServer(t, "10.0.0.5", 200, 0)
	s3 := echoServer(t, "127.0.0.1", 200, 0)

	r := newResolverFromServers([]*httptest.Server{s1, s2, s3}, 2, 2*time.Second)
	_, report, err := r.Resolve(context.Background())
	if !errors.Is(err, ddnserr.ErrNoQuorum) {
		t.Fatalf("err = %v, want ErrNoQuorum", err)
	}
	for _, s := range report.Sources {
		if !strings.HasPrefix(s.Error, "sanitize_reject:") {
			t.Fatalf("source %q Error = %q, want sanitize_reject: prefix", s.URL, s.Error)
		}
	}
}

// TestSanitize_PublicIPAccepted — sanity guard against over-eager
// rejection. A real-world public IP must return ok=true.
func TestSanitize_PublicIPAccepted(t *testing.T) {
	samples := []string{
		"192.0.2.42",
		"198.51.100.7",
		"203.0.113.9",
		"8.8.8.8",
		"1.1.1.1",
	}
	for _, s := range samples {
		addr, err := netip.ParseAddr(s)
		if err != nil {
			t.Fatalf("parse %s: %v", s, err)
		}
		ok, reason := sanitize(addr)
		if !ok {
			t.Fatalf("public IP %s rejected with reason %q", s, reason)
		}
	}
}
