//go:build !unix

package standalone

import "os"

// fileOwnerUID is a no-op fallback for non-Unix platforms.
func fileOwnerUID(_ os.FileInfo) (int, bool) {
	return 0, false
}
