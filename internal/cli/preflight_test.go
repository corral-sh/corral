package cli

import "testing"

// A pinned toolchain's doctor check matches the whole version token.
func TestHasVersion(t *testing.T) {
	for _, c := range []struct {
		out, version string
		want         bool
	}{
		{"go version go1.25.14 linux/arm64", "1.25.14", true},
		{"go version go1.25.14 linux/arm64", "1.25.1", false},
		{"go version go1.25.1 linux/arm64", "1.25", false},
		{"go version go1.20 linux/arm64", "1.20", true},
		{"v22.23.2", "22.23.2", true},
		{"v22.23.20", "22.23.2", false},
		{"v122.23.2", "22.23.2", false},
		{"Flutter 3.44.2 • channel stable • https://github.com/flutter/flutter.git", "3.44.2", true},
		{"Flutter 3.44.21 • channel stable", "3.44.2", false},
	} {
		if got := hasVersion(c.out, c.version); got != c.want {
			t.Errorf("hasVersion(%q, %q) = %v, want %v", c.out, c.version, got, c.want)
		}
	}
}
