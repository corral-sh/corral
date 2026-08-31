//go:build !darwin

package policy

// hostMemoryBytes returns 0 on hosts we do not probe; no cap is applied.
func hostMemoryBytes() uint64 { return 0 }
