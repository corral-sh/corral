//go:build !darwin

package lima

func cloneFile(src, dst string) error { return copyFile(src, dst) }
