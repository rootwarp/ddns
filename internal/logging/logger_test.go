package logging

import (
	"bytes"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/rootwarp/ddns/internal/version"
)

func TestNewLogger_Text(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger("text", &buf)
	logger.Info("hello", "k", "v")

	out := buf.String()
	// slog's text handler emits key=value pairs.
	if !strings.Contains(out, "msg=hello") {
		t.Fatalf("text output missing msg=hello: %q", out)
	}
	if !strings.Contains(out, "k=v") {
		t.Fatalf("text output missing k=v: %q", out)
	}
	if !strings.Contains(out, "version="+version.Version) {
		t.Fatalf("text output missing version attr: %q", out)
	}
	if !strings.Contains(out, "pid="+strconv.Itoa(os.Getpid())) {
		t.Fatalf("text output missing pid attr: %q", out)
	}
}

func TestNewLogger_JSON(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger("json", &buf)
	logger.Info("hello")

	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatalf("json output was empty")
	}

	var record map[string]any
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatalf("json output did not parse as JSON: %v (raw=%q)", err, line)
	}

	if got, want := record["msg"], "hello"; got != want {
		t.Fatalf("msg = %v, want %v", got, want)
	}
	if got, want := record["version"], version.Version; got != want {
		t.Fatalf("version attr = %v, want %v", got, want)
	}
	// slog JSON handler emits numeric pid as float64.
	pid, ok := record["pid"].(float64)
	if !ok {
		t.Fatalf("pid attr missing or wrong type: %T %v", record["pid"], record["pid"])
	}
	if int(pid) != os.Getpid() {
		t.Fatalf("pid = %v, want %v", int(pid), os.Getpid())
	}
}

func TestNewLogger_AutoNonTTY(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger("auto", &buf)
	logger.Info("hello")

	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatalf("auto output was empty")
	}
	// A *bytes.Buffer is not a *os.File, so auto should choose JSON.
	var record map[string]any
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatalf("auto-on-non-TTY did not produce JSON: %v (raw=%q)", err, line)
	}
}

func TestResolveFormat_ExplicitOverridesAuto(t *testing.T) {
	if got := resolveFormat("text", &bytes.Buffer{}); got != "text" {
		t.Fatalf("resolveFormat(text, buf) = %q, want text", got)
	}
	if got := resolveFormat("json", &bytes.Buffer{}); got != "json" {
		t.Fatalf("resolveFormat(json, buf) = %q, want json", got)
	}
}

func TestResolveFormat_AutoNonFileIsJSON(t *testing.T) {
	if got := resolveFormat("auto", &bytes.Buffer{}); got != "json" {
		t.Fatalf("resolveFormat(auto, non-file) = %q, want json", got)
	}
}

func TestResolveFormat_AutoNonTTYFileIsJSON(t *testing.T) {
	// An *os.File that isn't a terminal (a regular file) should select JSON.
	f, err := os.CreateTemp(t.TempDir(), "logtest-*.txt")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })

	if got := resolveFormat("auto", f); got != "json" {
		t.Fatalf("resolveFormat(auto, tempfile) = %q, want json", got)
	}
}
