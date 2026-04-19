package version

import (
	"strings"
	"testing"
)

// TestString_Defaults asserts that with the package-level default values,
// String() produces the documented format containing the "dev" version.
func TestString_Defaults(t *testing.T) {
	got := String()
	if !strings.Contains(got, "dev") {
		t.Fatalf("String() = %q, want to contain %q", got, "dev")
	}
	if !strings.Contains(got, "commit unknown") {
		t.Fatalf("String() = %q, want to contain %q", got, "commit unknown")
	}
	if !strings.Contains(got, "built unknown") {
		t.Fatalf("String() = %q, want to contain %q", got, "built unknown")
	}
}

// TestString_WithInjection mutates the package-level vars to simulate
// ldflags injection, asserts the format, and restores the originals in
// t.Cleanup so the test is order-independent.
func TestString_WithInjection(t *testing.T) {
	origVersion, origCommit, origBuildDate := Version, Commit, BuildDate
	t.Cleanup(func() {
		Version = origVersion
		Commit = origCommit
		BuildDate = origBuildDate
	})

	Version = "v0.1.0"
	Commit = "abc1234"
	BuildDate = "2026-04-19T12:00:00Z"

	got := String()
	want := "ddns v0.1.0 (commit abc1234, built 2026-04-19T12:00:00Z)"
	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
