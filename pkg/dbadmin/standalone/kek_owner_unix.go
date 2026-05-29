//go:build unix

package standalone

import (
	"os"
	"syscall"
)

// fileOwnerUID returns (uid, true) on Unix. The standalone target
// platforms (Linux, macOS) all satisfy this build tag.
func fileOwnerUID(st os.FileInfo) (int, bool) {
	if st == nil {
		return 0, false
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || sys == nil {
		return 0, false
	}
	return int(sys.Uid), true
}
