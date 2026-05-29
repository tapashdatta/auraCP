package standalone

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// NewLogger builds a slog.Logger per the standalone logging config.
// Destinations accepted:
//   - "stderr" (default)
//   - "stdout"
//   - "file:/absolute/path/to/file"
//
// Formats: "text" (default) or "json".
func NewLogger(level, format, destination string) (*slog.Logger, io.Closer, error) {
	var w io.Writer
	var closer io.Closer
	switch {
	case destination == "" || destination == "stderr":
		w = os.Stderr
	case destination == "stdout":
		w = os.Stdout
	case strings.HasPrefix(destination, "file:"):
		path := strings.TrimPrefix(destination, "file:")
		if path == "" {
			return nil, nil, fmt.Errorf("standalone: empty file: path")
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
		if err != nil {
			return nil, nil, err
		}
		w = f
		closer = f
	default:
		return nil, nil, fmt.Errorf("standalone: unknown log destination %q", destination)
	}

	opts := &slog.HandlerOptions{Level: parseLogLevel(level)}
	var h slog.Handler
	switch strings.ToLower(format) {
	case "", "text":
		h = slog.NewTextHandler(w, opts)
	case "json":
		h = slog.NewJSONHandler(w, opts)
	default:
		return nil, nil, fmt.Errorf("standalone: unknown log format %q", format)
	}
	return slog.New(h), closer, nil
}

func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
