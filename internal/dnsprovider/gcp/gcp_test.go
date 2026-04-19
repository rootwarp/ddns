// Tests for the Google Cloud DNS provider. Every test uses a mock
// http.RoundTripper so no test ever reaches the real Google API — the
// transport panics if a request hits a path we did not register.
//
// The mock matches on method + URL path suffix (the Google client builds
// absolute URLs under dns.googleapis.com, which we let stand, and we assert
// only on the path/method pair). Fixture bodies live under
// testdata/responses/.
package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/api/dns/v1"
	"google.golang.org/api/option"

	"github.com/rootwarp/ddns/internal/ddnserr"
	"github.com/rootwarp/ddns/internal/dnsprovider"
)

// --- mock transport helpers --------------------------------------------------

// route is one registered (method, pathSuffix) → fixture pairing. status is
// the HTTP status code to write; body is the raw JSON bytes. hits counts the
// number of requests served by this route, so tests can assert "zero calls"
// or "exactly one call" without depending on the transport's internal state.
type route struct {
	method     string
	pathSuffix string
	status     int
	body       []byte
	hits       *atomic.Int32
}

// newRoute reads a fixture file from testdata/responses/ and returns a route
// matching method+pathSuffix with the given HTTP status.
func newRoute(t *testing.T, method, pathSuffix, fixture string, status int) route {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "responses", fixture))
	if err != nil {
		t.Fatalf("read fixture %s: %v", fixture, err)
	}
	return route{
		method:     method,
		pathSuffix: pathSuffix,
		status:     status,
		body:       data,
		hits:       new(atomic.Int32),
	}
}

// mockTransport is a http.RoundTripper that serves pre-registered routes and
// panics on unexpected requests — the "panic on miss" behavior is what
// guarantees no test quietly escapes to the real network.
type mockTransport struct {
	t      *testing.T
	routes []route
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for i := range m.routes {
		r := &m.routes[i]
		if req.Method != r.method {
			continue
		}
		if !strings.HasSuffix(req.URL.Path, r.pathSuffix) {
			continue
		}
		r.hits.Add(1)
		resp := &http.Response{
			StatusCode: r.status,
			Status:     http.StatusText(r.status),
			Header:     http.Header{"Content-Type": []string{"application/json; charset=UTF-8"}},
			Body:       io.NopCloser(strings.NewReader(string(r.body))),
			Request:    req,
		}
		return resp, nil
	}
	m.t.Fatalf("unexpected HTTP call: %s %s", req.Method, req.URL.String())
	return nil, errors.New("unreachable")
}

// newMockProvider builds a *Provider wired to a mock http.Client.
func newMockProvider(t *testing.T, routes ...route) (*Provider, *mockTransport) {
	t.Helper()
	mt := &mockTransport{t: t, routes: routes}
	client := &http.Client{Transport: mt}
	svc, err := dns.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		t.Fatalf("dns.NewService: %v", err)
	}
	return newWithService(svc), mt
}

// --- New ---------------------------------------------------------------------

// TestNew_AuthMissing asserts that New returns an ErrAuth-wrapped error when
// ADC discovery cannot produce a credential. We force the failure by pointing
// GOOGLE_APPLICATION_CREDENTIALS at a path that does not exist; if the local
// ADC chain on this host still succeeds (e.g. a gcloud login session is
// active), the test skips rather than yielding a false pass.
func TestNew_AuthMissing(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/does/not/exist/ddns-test-adc.json")

	p, err := New(context.Background())
	if err == nil {
		// The ADC chain succeeded despite our bogus env — this host has a
		// live gcloud session or a GCE metadata server in reach. The issue
		// doc explicitly anticipates this and asks us to skip rather than
		// fail.
		t.Skipf("ADC chain unexpectedly succeeded (host has a live credential source); got provider %v", p)
	}
	if !errors.Is(err, ddnserr.ErrAuth) {
		t.Errorf("New error = %v, want one that wraps ErrAuth", err)
	}
}

// --- Get ---------------------------------------------------------------------

var testRef = dnsprovider.RecordRef{
	Project:     "my-project",
	ManagedZone: "example-com",
	Name:        "home.example.com.",
	Type:        "A",
}

func TestGet_OK(t *testing.T) {
	r := newRoute(t, http.MethodGet,
		"/projects/my-project/managedZones/example-com/rrsets/home.example.com./A",
		"get-ok.json", http.StatusOK)
	p, _ := newMockProvider(t, r)

	got, err := p.Get(context.Background(), testRef)
	if err != nil {
		t.Fatalf("Get err = %v, want nil", err)
	}
	if got.TTL != 300 {
		t.Errorf("Get TTL = %d, want 300", got.TTL)
	}
	if len(got.Rrdatas) != 1 || got.Rrdatas[0] != "198.51.100.7" {
		t.Errorf("Get Rrdatas = %v, want [198.51.100.7]", got.Rrdatas)
	}
	if r.hits.Load() != 1 {
		t.Errorf("Get route hits = %d, want 1", r.hits.Load())
	}
}

func TestGet_NotFound(t *testing.T) {
	r := newRoute(t, http.MethodGet,
		"/projects/my-project/managedZones/example-com/rrsets/home.example.com./A",
		"get-404.json", http.StatusNotFound)
	p, _ := newMockProvider(t, r)

	_, err := p.Get(context.Background(), testRef)
	if !errors.Is(err, ddnserr.ErrNotFound) {
		t.Errorf("Get err = %v, want ErrNotFound", err)
	}
	if errors.Is(err, ddnserr.ErrAuth) || errors.Is(err, ddnserr.ErrTransient) {
		t.Errorf("Get 404 err = %v must not also be ErrAuth/ErrTransient", err)
	}
}

func TestGet_PermissionDenied(t *testing.T) {
	r := newRoute(t, http.MethodGet,
		"/projects/my-project/managedZones/example-com/rrsets/home.example.com./A",
		"get-403.json", http.StatusForbidden)
	p, _ := newMockProvider(t, r)

	_, err := p.Get(context.Background(), testRef)
	if !errors.Is(err, ddnserr.ErrAuth) {
		t.Errorf("Get err = %v, want one that wraps ErrAuth", err)
	}
}

func TestGet_Transient5xx(t *testing.T) {
	// Reuse the 429 body — exact fixture content doesn't matter, only status.
	r := newRoute(t, http.MethodGet,
		"/projects/my-project/managedZones/example-com/rrsets/home.example.com./A",
		"upsert-429.json", http.StatusServiceUnavailable)
	p, _ := newMockProvider(t, r)

	_, err := p.Get(context.Background(), testRef)
	if !errors.Is(err, ddnserr.ErrTransient) {
		t.Errorf("Get err = %v, want one that wraps ErrTransient", err)
	}
}

// --- Upsert ------------------------------------------------------------------

func TestUpsert_Create(t *testing.T) {
	getRoute := newRoute(t, http.MethodGet,
		"/projects/my-project/managedZones/example-com/rrsets/home.example.com./A",
		"get-404.json", http.StatusNotFound)
	createRoute := newRoute(t, http.MethodPost,
		"/projects/my-project/managedZones/example-com/changes",
		"upsert-create-ok.json", http.StatusOK)

	// Capture the posted body so we can assert additions-only (no deletions).
	captured := &capturingRoute{r: createRoute}
	p := newMockProviderWithCapture(t, []route{getRoute}, captured)

	res, err := p.Upsert(context.Background(), testRef, dnsprovider.Record{
		Rrdatas: []string{"192.0.2.42"},
		TTL:     300,
	})
	if err != nil {
		t.Fatalf("Upsert err = %v", err)
	}
	if !res.Changed {
		t.Errorf("UpsertResult.Changed = false, want true")
	}
	if len(res.OldRrdatas) != 0 {
		t.Errorf("UpsertResult.OldRrdatas = %v, want empty", res.OldRrdatas)
	}
	if len(res.NewRrdatas) != 1 || res.NewRrdatas[0] != "192.0.2.42" {
		t.Errorf("UpsertResult.NewRrdatas = %v, want [192.0.2.42]", res.NewRrdatas)
	}

	var sent dns.Change
	if err := json.Unmarshal(captured.reqBody, &sent); err != nil {
		t.Fatalf("unmarshal posted body: %v\nbody: %s", err, captured.reqBody)
	}
	if len(sent.Deletions) != 0 {
		t.Errorf("create path sent Deletions = %v, want none", sent.Deletions)
	}
	if len(sent.Additions) != 1 {
		t.Fatalf("create path Additions len = %d, want 1", len(sent.Additions))
	}
	add := sent.Additions[0]
	if add.Name != "home.example.com." || add.Type != "A" || add.Ttl != 300 || len(add.Rrdatas) != 1 || add.Rrdatas[0] != "192.0.2.42" {
		t.Errorf("Additions[0] = %+v", add)
	}
}

func TestUpsert_Update(t *testing.T) {
	getRoute := newRoute(t, http.MethodGet,
		"/projects/my-project/managedZones/example-com/rrsets/home.example.com./A",
		"get-ok.json", http.StatusOK)
	createRoute := newRoute(t, http.MethodPost,
		"/projects/my-project/managedZones/example-com/changes",
		"upsert-update-ok.json", http.StatusOK)

	captured := &capturingRoute{r: createRoute}
	p := newMockProviderWithCapture(t, []route{getRoute}, captured)

	res, err := p.Upsert(context.Background(), testRef, dnsprovider.Record{
		Rrdatas: []string{"192.0.2.42"},
		TTL:     300,
	})
	if err != nil {
		t.Fatalf("Upsert err = %v", err)
	}
	if !res.Changed {
		t.Errorf("UpsertResult.Changed = false, want true")
	}
	if len(res.OldRrdatas) != 1 || res.OldRrdatas[0] != "198.51.100.7" {
		t.Errorf("OldRrdatas = %v, want [198.51.100.7]", res.OldRrdatas)
	}
	if len(res.NewRrdatas) != 1 || res.NewRrdatas[0] != "192.0.2.42" {
		t.Errorf("NewRrdatas = %v, want [192.0.2.42]", res.NewRrdatas)
	}

	var sent dns.Change
	if err := json.Unmarshal(captured.reqBody, &sent); err != nil {
		t.Fatalf("unmarshal posted body: %v\nbody: %s", err, captured.reqBody)
	}
	if len(sent.Deletions) != 1 {
		t.Fatalf("Deletions len = %d, want 1", len(sent.Deletions))
	}
	del := sent.Deletions[0]
	if del.Name != "home.example.com." {
		t.Errorf("Deletions[0].Name = %q, want %q", del.Name, "home.example.com.")
	}
	if del.Type != "A" {
		t.Errorf("Deletions[0].Type = %q, want %q", del.Type, "A")
	}
	if del.Ttl != 300 {
		t.Errorf("Deletions[0].Ttl = %d, want 300", del.Ttl)
	}
	if len(del.Rrdatas) != 1 || del.Rrdatas[0] != "198.51.100.7" {
		t.Errorf("Deletions[0].Rrdatas = %v, want [198.51.100.7]", del.Rrdatas)
	}
	if len(sent.Additions) != 1 {
		t.Fatalf("Additions len = %d, want 1", len(sent.Additions))
	}
	add := sent.Additions[0]
	if add.Name != "home.example.com." || add.Type != "A" || add.Ttl != 300 {
		t.Errorf("Additions[0] header mismatch: %+v", add)
	}
	if len(add.Rrdatas) != 1 || add.Rrdatas[0] != "192.0.2.42" {
		t.Errorf("Additions[0].Rrdatas = %v, want [192.0.2.42]", add.Rrdatas)
	}
}

func TestUpsert_NoOp(t *testing.T) {
	getRoute := newRoute(t, http.MethodGet,
		"/projects/my-project/managedZones/example-com/rrsets/home.example.com./A",
		"get-ok.json", http.StatusOK)
	// Register a Changes.Create route so we can count calls — but we expect
	// zero hits. If the code wrongly POSTs, the mock won't panic (the route
	// exists) but the hits counter will reveal the bug.
	createRoute := newRoute(t, http.MethodPost,
		"/projects/my-project/managedZones/example-com/changes",
		"upsert-update-ok.json", http.StatusOK)
	p, _ := newMockProvider(t, getRoute, createRoute)

	// Ask for the exact same rrdata+TTL the get-ok.json fixture returns.
	res, err := p.Upsert(context.Background(), testRef, dnsprovider.Record{
		Rrdatas: []string{"198.51.100.7"},
		TTL:     300,
	})
	if err != nil {
		t.Fatalf("Upsert err = %v", err)
	}
	if res.Changed {
		t.Errorf("UpsertResult.Changed = true, want false")
	}
	if len(res.OldRrdatas) != 1 || res.OldRrdatas[0] != "198.51.100.7" {
		t.Errorf("OldRrdatas = %v", res.OldRrdatas)
	}
	if len(res.NewRrdatas) != 1 || res.NewRrdatas[0] != "198.51.100.7" {
		t.Errorf("NewRrdatas = %v", res.NewRrdatas)
	}
	if createRoute.hits.Load() != 0 {
		t.Errorf("Changes.Create hits = %d, want 0 (no-op must not POST)", createRoute.hits.Load())
	}
}

// TestUpsert_NoOp_UnorderedRrdatas guards the sameRrdatas helper contract:
// Cloud DNS does not promise Rrdatas order on Get, so a live response with
// the same set in a different order must still be treated as a no-op.
func TestUpsert_NoOp_UnorderedRrdatas(t *testing.T) {
	// Custom fixture written inline so we can pack multiple values.
	body := []byte(`{
      "kind":"dns#resourceRecordSet","name":"home.example.com.","type":"A","ttl":300,
      "rrdatas":["198.51.100.7","192.0.2.1"]
    }`)
	getRoute := route{
		method:     http.MethodGet,
		pathSuffix: "/projects/my-project/managedZones/example-com/rrsets/home.example.com./A",
		status:     http.StatusOK,
		body:       body,
		hits:       new(atomic.Int32),
	}
	createRoute := newRoute(t, http.MethodPost,
		"/projects/my-project/managedZones/example-com/changes",
		"upsert-update-ok.json", http.StatusOK)
	p, _ := newMockProvider(t, getRoute, createRoute)

	// Caller asks in a different order — still the same set.
	res, err := p.Upsert(context.Background(), testRef, dnsprovider.Record{
		Rrdatas: []string{"192.0.2.1", "198.51.100.7"},
		TTL:     300,
	})
	if err != nil {
		t.Fatalf("Upsert err = %v", err)
	}
	if res.Changed {
		t.Errorf("Changed = true, want false (same set, different order)")
	}
	if createRoute.hits.Load() != 0 {
		t.Errorf("Changes.Create hits = %d, want 0", createRoute.hits.Load())
	}
}

// TestUpsert_TTLChangeTriggersWrite ensures the old.TTL == new.TTL check is
// honored: same rrdatas + different TTL → Changed=true, POST issued.
func TestUpsert_TTLChangeTriggersWrite(t *testing.T) {
	getRoute := newRoute(t, http.MethodGet,
		"/projects/my-project/managedZones/example-com/rrsets/home.example.com./A",
		"get-ok.json", http.StatusOK)
	createRoute := newRoute(t, http.MethodPost,
		"/projects/my-project/managedZones/example-com/changes",
		"upsert-update-ok.json", http.StatusOK)
	p, _ := newMockProvider(t, getRoute, createRoute)

	// Same rrdatas as get-ok.json, but TTL bumped from 300 to 600.
	res, err := p.Upsert(context.Background(), testRef, dnsprovider.Record{
		Rrdatas: []string{"198.51.100.7"},
		TTL:     600,
	})
	if err != nil {
		t.Fatalf("Upsert err = %v", err)
	}
	if !res.Changed {
		t.Errorf("Changed = false, want true (TTL differs)")
	}
	if createRoute.hits.Load() != 1 {
		t.Errorf("Changes.Create hits = %d, want 1", createRoute.hits.Load())
	}
}

func TestUpsert_PermissionDenied(t *testing.T) {
	getRoute := newRoute(t, http.MethodGet,
		"/projects/my-project/managedZones/example-com/rrsets/home.example.com./A",
		"get-ok.json", http.StatusOK)
	createRoute := newRoute(t, http.MethodPost,
		"/projects/my-project/managedZones/example-com/changes",
		"upsert-403.json", http.StatusForbidden)
	p, _ := newMockProvider(t, getRoute, createRoute)

	_, err := p.Upsert(context.Background(), testRef, dnsprovider.Record{
		Rrdatas: []string{"192.0.2.42"},
		TTL:     300,
	})
	if !errors.Is(err, ddnserr.ErrAuth) {
		t.Errorf("Upsert err = %v, want ErrAuth", err)
	}
}

func TestUpsert_RateLimited(t *testing.T) {
	getRoute := newRoute(t, http.MethodGet,
		"/projects/my-project/managedZones/example-com/rrsets/home.example.com./A",
		"get-ok.json", http.StatusOK)
	createRoute := newRoute(t, http.MethodPost,
		"/projects/my-project/managedZones/example-com/changes",
		"upsert-429.json", http.StatusTooManyRequests)
	p, _ := newMockProvider(t, getRoute, createRoute)

	_, err := p.Upsert(context.Background(), testRef, dnsprovider.Record{
		Rrdatas: []string{"192.0.2.42"},
		TTL:     300,
	})
	if !errors.Is(err, ddnserr.ErrTransient) {
		t.Errorf("Upsert err = %v, want ErrTransient", err)
	}
}

func TestUpsert_PendingStatus(t *testing.T) {
	getRoute := newRoute(t, http.MethodGet,
		"/projects/my-project/managedZones/example-com/rrsets/home.example.com./A",
		"get-404.json", http.StatusNotFound)
	createRoute := newRoute(t, http.MethodPost,
		"/projects/my-project/managedZones/example-com/changes",
		"upsert-pending.json", http.StatusOK)
	p, _ := newMockProvider(t, getRoute, createRoute)

	res, err := p.Upsert(context.Background(), testRef, dnsprovider.Record{
		Rrdatas: []string{"192.0.2.42"},
		TTL:     300,
	})
	if err != nil {
		t.Fatalf("Upsert err = %v", err)
	}
	if !res.Changed {
		t.Errorf("Changed = false, want true (pending is success)")
	}
}

func TestUpsert_ZoneNotFound(t *testing.T) {
	// Get succeeds (zone currently exists), but between Get and Create the
	// zone is deleted — Changes.Create returns 404.
	getRoute := newRoute(t, http.MethodGet,
		"/projects/my-project/managedZones/example-com/rrsets/home.example.com./A",
		"get-ok.json", http.StatusOK)
	createRoute := newRoute(t, http.MethodPost,
		"/projects/my-project/managedZones/example-com/changes",
		"get-404.json", http.StatusNotFound)
	p, _ := newMockProvider(t, getRoute, createRoute)

	_, err := p.Upsert(context.Background(), testRef, dnsprovider.Record{
		Rrdatas: []string{"192.0.2.42"},
		TTL:     300,
	})
	if !errors.Is(err, ddnserr.ErrNotFound) {
		t.Errorf("Upsert err = %v, want ErrNotFound", err)
	}
}

func TestUpsert_PreconditionFailed(t *testing.T) {
	// Mid-flight race: the Get-observed RRset was edited by someone else
	// before our Changes.Create landed. Cloud DNS returns 412 Precondition
	// Failed. We map it to ErrTransient so the next tick retries with a
	// fresh Get.
	getRoute := newRoute(t, http.MethodGet,
		"/projects/my-project/managedZones/example-com/rrsets/home.example.com./A",
		"get-ok.json", http.StatusOK)
	createRoute := newRoute(t, http.MethodPost,
		"/projects/my-project/managedZones/example-com/changes",
		"upsert-429.json", http.StatusPreconditionFailed)
	p, _ := newMockProvider(t, getRoute, createRoute)

	_, err := p.Upsert(context.Background(), testRef, dnsprovider.Record{
		Rrdatas: []string{"192.0.2.42"},
		TTL:     300,
	})
	if !errors.Is(err, ddnserr.ErrTransient) {
		t.Errorf("Upsert err = %v, want ErrTransient", err)
	}
}

// TestUpsert_GetPropagatesNon404Error covers the branch where the internal
// Get returns an error that is not ErrNotFound — Upsert must not swallow it
// and must not proceed to Changes.Create.
func TestUpsert_GetPropagatesNon404Error(t *testing.T) {
	// Get returns 500 → classified as ErrTransient.
	getRoute := newRoute(t, http.MethodGet,
		"/projects/my-project/managedZones/example-com/rrsets/home.example.com./A",
		"upsert-429.json", http.StatusInternalServerError)
	// Register Changes.Create so we can confirm it's NOT hit.
	createRoute := newRoute(t, http.MethodPost,
		"/projects/my-project/managedZones/example-com/changes",
		"upsert-update-ok.json", http.StatusOK)
	p, _ := newMockProvider(t, getRoute, createRoute)

	_, err := p.Upsert(context.Background(), testRef, dnsprovider.Record{
		Rrdatas: []string{"192.0.2.42"},
		TTL:     300,
	})
	if !errors.Is(err, ddnserr.ErrTransient) {
		t.Errorf("Upsert err = %v, want ErrTransient", err)
	}
	if createRoute.hits.Load() != 0 {
		t.Errorf("Changes.Create hits = %d, want 0 (Get failure must short-circuit)", createRoute.hits.Load())
	}
}

// TestSameRrdatas_LengthDifference exercises the fast-path length check in
// the sameRrdatas helper. A no-op test at the public surface can't hit this
// path with a single-element rrdata — the daemon always asks for exactly
// one IP — so we use the helper directly here.
func TestSameRrdatas_LengthDifference(t *testing.T) {
	if sameRrdatas([]string{"a"}, []string{"a", "b"}) {
		t.Errorf("sameRrdatas with different lengths reported equal")
	}
	if !sameRrdatas([]string{"a", "b"}, []string{"b", "a"}) {
		t.Errorf("sameRrdatas ignored sort on same-set input")
	}
}

// --- programmer-error invariants --------------------------------------------

func TestUpsert_EmptyRrdatas_IsBug(t *testing.T) {
	p, _ := newMockProvider(t) // no routes — a correct impl must not even Get.

	_, err := p.Upsert(context.Background(), testRef, dnsprovider.Record{
		Rrdatas: nil,
		TTL:     300,
	})
	if err == nil || !strings.HasPrefix(err.Error(), "ddns bug:") {
		t.Errorf("Upsert err = %v, want one prefixed 'ddns bug:'", err)
	}
}

func TestUpsert_NonIPv4_IsBug(t *testing.T) {
	p, _ := newMockProvider(t)

	_, err := p.Upsert(context.Background(), testRef, dnsprovider.Record{
		Rrdatas: []string{"not-an-ip"},
		TTL:     300,
	})
	if err == nil || !strings.HasPrefix(err.Error(), "ddns bug:") {
		t.Errorf("Upsert err = %v, want one prefixed 'ddns bug:'", err)
	}
}

func TestUpsert_NonARecord_IsBug(t *testing.T) {
	p, _ := newMockProvider(t)

	ref := testRef
	ref.Type = "AAAA"
	_, err := p.Upsert(context.Background(), ref, dnsprovider.Record{
		Rrdatas: []string{"192.0.2.1"},
		TTL:     300,
	})
	if err == nil || !strings.HasPrefix(err.Error(), "ddns bug:") {
		t.Errorf("Upsert err = %v, want one prefixed 'ddns bug:'", err)
	}
}

// --- capturing helper --------------------------------------------------------

// capturingRoute bundles a route with a slot for the request body captured
// on the first matching call. `reqBody` is the POST payload we saw; the
// response body is always the route's fixture.
type capturingRoute struct {
	r       route
	reqBody []byte
}

// captureTransport intercepts requests matching capture.r (reading and
// stashing the request body) before falling back to the base mockTransport
// for everything else.
type captureTransport struct {
	base    *mockTransport
	capture *capturingRoute
}

func (c *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == c.capture.r.method && strings.HasSuffix(req.URL.Path, c.capture.r.pathSuffix) {
		if req.Body != nil {
			b, err := io.ReadAll(req.Body)
			if err != nil {
				c.base.t.Fatalf("read posted body: %v", err)
			}
			c.capture.reqBody = b
		}
		c.capture.r.hits.Add(1)
		return &http.Response{
			StatusCode: c.capture.r.status,
			Status:     http.StatusText(c.capture.r.status),
			Header:     http.Header{"Content-Type": []string{"application/json; charset=UTF-8"}},
			Body:       io.NopCloser(strings.NewReader(string(c.capture.r.body))),
			Request:    req,
		}, nil
	}
	return c.base.RoundTrip(req)
}

// newMockProviderWithCapture wires up a Provider whose HTTP client records
// the body of the first request matching capture.r.
func newMockProviderWithCapture(t *testing.T, routes []route, capture *capturingRoute) *Provider {
	t.Helper()
	ct := &captureTransport{
		base:    &mockTransport{t: t, routes: routes},
		capture: capture,
	}
	client := &http.Client{Transport: ct}
	svc, err := dns.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		t.Fatalf("dns.NewService: %v", err)
	}
	return newWithService(svc)
}
