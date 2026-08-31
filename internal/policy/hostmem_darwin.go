package policy

import "golang.org/x/sys/unix"

// hostMemoryBytes returns physical RAM, or 0 if unknown.
func hostMemoryBytes() uint64 {
	v, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0
	}
	return v
}
