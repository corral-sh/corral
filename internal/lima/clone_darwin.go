//go:build darwin

package lima

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// cloneFile makes a copy-on-write clone on APFS; on other filesystems it
// falls back to a full copy.
func cloneFile(src, dst string) error {
	err := unix.Clonefile(src, dst, 0)
	if err == nil {
		return nil
	}
	if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EXDEV) {
		return copyFile(src, dst)
	}
	if errors.Is(err, unix.EEXIST) {
		_ = os.Remove(dst)
		return unix.Clonefile(src, dst, 0)
	}
	return err
}
