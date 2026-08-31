package lima

import (
	"os"
	"syscall"
)

// allocatedSize returns the on-disk size of a (possibly sparse) file.
func allocatedSize(info os.FileInfo) int64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return st.Blocks * 512
	}
	return info.Size()
}
