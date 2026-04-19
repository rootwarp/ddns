package logging

import (
	"io"
	"log/slog"
	"os"

	"golang.org/x/term"

	"github.com/rootwarp/ddns/internal/version"
)

// NewLogger constructs a *slog.Logger using the requested format.
//
// format accepts "text", "json", or "auto". "auto" picks text when w is a
// terminal *os.File, and JSON otherwise — which is the behavior you want
// for an interactive shell vs. a systemd/pipe/file sink.
//
// The returned logger carries default attrs `version` (from internal/version)
// and `pid` so every record is greppable after the fact.
//
// Level is hard-coded to INFO for v1.
func NewLogger(format string, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}

	var handler slog.Handler
	switch resolveFormat(format, w) {
	case "json":
		handler = slog.NewJSONHandler(w, opts)
	default:
		handler = slog.NewTextHandler(w, opts)
	}
	return slog.New(handler).With("version", version.Version, "pid", os.Getpid())
}

// resolveFormat maps the user-supplied format to the concrete "text" or "json"
// handler name. Explicit values pass through; "auto" (or anything unknown)
// inspects whether w is a TTY.
func resolveFormat(format string, w io.Writer) string {
	switch format {
	case "text", "json":
		return format
	}
	if f, ok := w.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		return "text"
	}
	return "json"
}
