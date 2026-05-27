// Package logs reads the tail of a site's log files for the Logs tab.
package logs

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/auracp/auracp/internal/paths"
	"github.com/auracp/auracp/internal/validate"
)

const maxTailBytes = 256 * 1024 // read at most the last 256 KB

// Tail returns the last n lines of a site's log of the given kind.
// kind is restricted to a known set to prevent path traversal.
func Tail(user, kind string, n int) ([]string, error) {
	if err := validate.Username(user); err != nil {
		return nil, err
	}
	name := map[string]string{
		"access": "access.log",
		"error":  "error.log",
		"app":    "app.log",
	}[kind]
	if name == "" {
		name = "access.log"
	}
	path := filepath.Join(paths.LogDir(user), name)

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	start := int64(0)
	if fi.Size() > maxTailBytes {
		start = fi.Size() - maxTailBytes
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if start > 0 && len(lines) > 0 {
		lines = lines[1:] // drop the partial first line
	}
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}
