package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rootwarp/ddns/internal/ddnserr"
)

// TestLoad_ValidReturnsPopulated ensures the canonical valid fixture loads
// cleanly and carries the values the user wrote.
func TestLoad_ValidReturnsPopulated(t *testing.T) {
	cfg, err := Load("testdata/valid.yaml")
	if err != nil {
		t.Fatalf("Load(valid.yaml) error: %v", err)
	}
	if cfg == nil {
		t.Fatal("Load returned nil config")
	}
	if got, want := cfg.PollInterval, 5*time.Minute; got != want {
		t.Errorf("PollInterval = %v, want %v", got, want)
	}
	if cfg.LogFormat != "auto" {
		t.Errorf("LogFormat = %q, want auto", cfg.LogFormat)
	}
	if cfg.Resolver.Quorum != 2 {
		t.Errorf("Resolver.Quorum = %d, want 2", cfg.Resolver.Quorum)
	}
	if len(cfg.Resolver.Sources) != 3 {
		t.Errorf("Resolver.Sources len = %d, want 3", len(cfg.Resolver.Sources))
	}
	if got, want := cfg.Resolver.Timeout, 10*time.Second; got != want {
		t.Errorf("Resolver.Timeout = %v, want %v", got, want)
	}
	if len(cfg.Records) != 1 {
		t.Fatalf("Records len = %d, want 1", len(cfg.Records))
	}
	rec := cfg.Records[0]
	if rec.Project != "my-home-lab" {
		t.Errorf("Records[0].Project = %q, want my-home-lab", rec.Project)
	}
	if rec.ManagedZone != "example-com" {
		t.Errorf("Records[0].ManagedZone = %q, want example-com", rec.ManagedZone)
	}
	if rec.Name != "home.example.com." {
		t.Errorf("Records[0].Name = %q, want home.example.com.", rec.Name)
	}
	if rec.Type != "A" {
		t.Errorf("Records[0].Type = %q, want A", rec.Type)
	}
	if rec.TTL != 300 {
		t.Errorf("Records[0].TTL = %d, want 300", rec.TTL)
	}
	// StatePath should be expanded from ~/.local/state/ddns
	if strings.HasPrefix(cfg.StatePath, "~") {
		t.Errorf("StatePath still starts with ~: %q", cfg.StatePath)
	}
}

// TestLoad_PathAccessor confirms Config retains the load path for Phase-4 SIGHUP.
func TestLoad_PathAccessor(t *testing.T) {
	cfg, err := Load("testdata/valid.yaml")
	if err != nil {
		t.Fatalf("Load(valid.yaml) error: %v", err)
	}
	if cfg.Path() != "testdata/valid.yaml" {
		t.Errorf("Path() = %q, want testdata/valid.yaml", cfg.Path())
	}
}

// TestLoad_InvalidTTLWrapsErrConfig asserts the error chain includes ErrConfig.
func TestLoad_InvalidTTLWrapsErrConfig(t *testing.T) {
	_, err := Load("testdata/invalid-ttl.yaml")
	if err == nil {
		t.Fatal("Load(invalid-ttl.yaml): expected error, got nil")
	}
	if !errors.Is(err, ddnserr.ErrConfig) {
		t.Errorf("err does not wrap ddnserr.ErrConfig: %v", err)
	}
	if !strings.Contains(err.Error(), "ttl") && !strings.Contains(err.Error(), "TTL") {
		t.Errorf("err does not mention TTL: %v", err)
	}
}

// TestLoad_InvalidTypeMessage checks the v1 "only A records" message.
func TestLoad_InvalidTypeMessage(t *testing.T) {
	_, err := Load("testdata/invalid-type.yaml")
	if err == nil {
		t.Fatal("Load(invalid-type.yaml): expected error, got nil")
	}
	if !errors.Is(err, ddnserr.ErrConfig) {
		t.Errorf("err does not wrap ddnserr.ErrConfig: %v", err)
	}
	if !strings.Contains(err.Error(), "only A records are supported") {
		t.Errorf("err missing expected message: %v", err)
	}
}

// TestLoad_MissingRecordsErrors asserts non-empty records requirement.
func TestLoad_MissingRecordsErrors(t *testing.T) {
	_, err := Load("testdata/missing-records.yaml")
	if err == nil {
		t.Fatal("Load(missing-records.yaml): expected error, got nil")
	}
	if !errors.Is(err, ddnserr.ErrConfig) {
		t.Errorf("err does not wrap ddnserr.ErrConfig: %v", err)
	}
	if !strings.Contains(err.Error(), "records") {
		t.Errorf("err missing 'records' mention: %v", err)
	}
}

// TestLoad_MultiErrorAggregates verifies that three problems surface in one error.
func TestLoad_MultiErrorAggregates(t *testing.T) {
	_, err := Load("testdata/multi-error.yaml")
	if err == nil {
		t.Fatal("Load(multi-error.yaml): expected error, got nil")
	}
	if !errors.Is(err, ddnserr.ErrConfig) {
		t.Errorf("err does not wrap ddnserr.ErrConfig: %v", err)
	}
	msg := err.Error()
	// Three distinct problems: quorum>sources, empty project, type!=A.
	checks := []string{"quorum", "project", "only A records are supported"}
	for _, s := range checks {
		if !strings.Contains(msg, s) {
			t.Errorf("multi-error message missing %q; got: %v", s, msg)
		}
	}
}

// TestLoad_DuplicateNamesRejected covers normalization-aware duplicate detection.
func TestLoad_DuplicateNamesRejected(t *testing.T) {
	_, err := Load("testdata/duplicate-names.yaml")
	if err == nil {
		t.Fatal("Load(duplicate-names.yaml): expected error, got nil")
	}
	if !errors.Is(err, ddnserr.ErrConfig) {
		t.Errorf("err does not wrap ddnserr.ErrConfig: %v", err)
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("err missing 'duplicate': %v", err)
	}
}

// TestLoad_TrailingDotNormalization ensures home.example.com becomes home.example.com.
func TestLoad_TrailingDotNormalization(t *testing.T) {
	cfg, err := Load("testdata/trailing-dot-normalization.yaml")
	if err != nil {
		t.Fatalf("Load(trailing-dot-normalization.yaml): %v", err)
	}
	if cfg.Records[0].Name != "home.example.com." {
		t.Errorf("Name not normalized: got %q, want home.example.com.", cfg.Records[0].Name)
	}
}

// TestLoad_FileMissingIsError covers the ReadFile failure path.
func TestLoad_FileMissingIsError(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err == nil {
		t.Fatal("Load(missing): expected error, got nil")
	}
}

// TestLoad_MalformedYAMLIsError covers YAML parse failure.
func TestLoad_MalformedYAMLIsError(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(bad, []byte("records: [unclosed"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err := Load(bad)
	if err == nil {
		t.Fatal("Load(malformed): expected error, got nil")
	}
}

// TestLoad_DefaultsApplyWhenFieldsOmitted feeds a tiny config that omits almost
// everything, and proves defaults (PollInterval, LogFormat, Resolver.*,
// record Type/TTL) are populated.
func TestLoad_DefaultsApplyWhenFieldsOmitted(t *testing.T) {
	minimal := filepath.Join(t.TempDir(), "minimal.yaml")
	content := `records:
  - project: p
    managed_zone: z
    name: x.example.com.
`
	if err := os.WriteFile(minimal, []byte(content), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	cfg, err := Load(minimal)
	if err != nil {
		t.Fatalf("Load(minimal): %v", err)
	}
	if cfg.PollInterval != 5*time.Minute {
		t.Errorf("PollInterval default = %v, want 5m", cfg.PollInterval)
	}
	if cfg.LogFormat != "auto" {
		t.Errorf("LogFormat default = %q, want auto", cfg.LogFormat)
	}
	if cfg.Resolver.Quorum != 2 {
		t.Errorf("Resolver.Quorum default = %d, want 2", cfg.Resolver.Quorum)
	}
	if cfg.Resolver.Timeout != 10*time.Second {
		t.Errorf("Resolver.Timeout default = %v, want 10s", cfg.Resolver.Timeout)
	}
	wantSources := []string{
		"https://api.ipify.org",
		"https://ifconfig.me/ip",
		"https://icanhazip.com",
	}
	if len(cfg.Resolver.Sources) != len(wantSources) {
		t.Fatalf("Resolver.Sources = %v, want %v", cfg.Resolver.Sources, wantSources)
	}
	for i, s := range wantSources {
		if cfg.Resolver.Sources[i] != s {
			t.Errorf("Resolver.Sources[%d] = %q, want %q", i, cfg.Resolver.Sources[i], s)
		}
	}
	if cfg.Records[0].Type != "A" {
		t.Errorf("Records[0].Type default = %q, want A", cfg.Records[0].Type)
	}
	if cfg.Records[0].TTL != 300 {
		t.Errorf("Records[0].TTL default = %d, want 300", cfg.Records[0].TTL)
	}
	// Still normalized
	if cfg.Records[0].Name != "x.example.com." {
		t.Errorf("Records[0].Name = %q, want x.example.com.", cfg.Records[0].Name)
	}
}

// TestLoad_TTLBounds is a table-driven scan of TTL boundary values.
func TestLoad_TTLBounds(t *testing.T) {
	cases := []struct {
		name    string
		ttl     int64
		wantErr bool
	}{
		{"lower-boundary-valid", 30, false},
		{"upper-boundary-valid", 86400, false},
		{"below-lower", 29, true},
		{"above-upper", 86401, true},
		{"zero-becomes-default", 0, false}, // 0 triggers default 300
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "cfg.yaml")
			content := `records:
  - project: p
    managed_zone: z
    name: x.example.com.
    type: A
    ttl: ` + itoa(tc.ttl) + "\n"
			if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
				t.Fatalf("setup: %v", err)
			}
			_, err := Load(p)
			if tc.wantErr && err == nil {
				t.Errorf("ttl=%d: expected error", tc.ttl)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("ttl=%d: unexpected error: %v", tc.ttl, err)
			}
		})
	}
}

// TestLoad_QuorumBounds tests quorum validation relative to sources len.
func TestLoad_QuorumBounds(t *testing.T) {
	cases := []struct {
		name    string
		quorum  int
		sources int
		wantErr bool
	}{
		{"quorum-equals-sources", 2, 2, false},
		{"quorum-less-than-sources", 1, 2, false},
		{"quorum-greater-than-sources", 3, 2, true},
		{"quorum-zero-becomes-default", 0, 3, false},
		{"quorum-2-but-only-1-source", 2, 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "cfg.yaml")
			sb := strings.Builder{}
			sb.WriteString("resolver:\n  sources:\n")
			for i := 0; i < tc.sources; i++ {
				sb.WriteString("    - https://s" + itoa(int64(i)) + ".example.com\n")
			}
			sb.WriteString("  quorum: " + itoa(int64(tc.quorum)) + "\n")
			sb.WriteString("records:\n  - project: p\n    managed_zone: z\n    name: x.example.com.\n    type: A\n    ttl: 300\n")
			if err := os.WriteFile(p, []byte(sb.String()), 0o600); err != nil {
				t.Fatalf("setup: %v", err)
			}
			_, err := Load(p)
			if tc.wantErr && err == nil {
				t.Errorf("quorum=%d sources=%d: expected error", tc.quorum, tc.sources)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("quorum=%d sources=%d: unexpected error: %v", tc.quorum, tc.sources, err)
			}
		})
	}
}

// TestLoad_EmptyProjectZoneNameRejected asserts per-record required fields.
func TestLoad_EmptyProjectZoneNameRejected(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"empty-project", "records:\n  - project: ''\n    managed_zone: z\n    name: x.example.com.\n"},
		{"empty-zone", "records:\n  - project: p\n    managed_zone: ''\n    name: x.example.com.\n"},
		{"empty-name", "records:\n  - project: p\n    managed_zone: z\n    name: ''\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "cfg.yaml")
			if err := os.WriteFile(p, []byte(tc.body), 0o600); err != nil {
				t.Fatalf("setup: %v", err)
			}
			_, err := Load(p)
			if err == nil {
				t.Fatalf("%s: expected error", tc.name)
			}
			if !errors.Is(err, ddnserr.ErrConfig) {
				t.Errorf("%s: does not wrap ErrConfig: %v", tc.name, err)
			}
		})
	}
}

// itoa converts int64 to string without importing strconv at the top of test
// files that already pull enough formatting helpers — keeps the imports tight.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
