package lima

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanLogLine(t *testing.T) {
	in := `time="2026-08-25T10:31:46+08:00" level=info msg="READY. Run \"limactl shell\" now."`
	if got := cleanLogLine(in); got != `READY. Run "limactl shell" now.` {
		t.Errorf("got %q", got)
	}
	in = `time="x" level=warning msg="disk full"`
	if got := cleanLogLine(in); got != "Warning: disk full" {
		t.Errorf("got %q", got)
	}
	if got := cleanLogLine("  plain  "); got != "plain" {
		t.Errorf("got %q", got)
	}
}

func TestVersionAtLeast(t *testing.T) {
	cases := []struct {
		have, want string
		ok         bool
	}{
		{"2.2.0", "1.0.0", true},
		{"1.0.0", "1.0.0", true},
		{"0.23.2", "1.0.0", false},
		{"limactl version 2.2.0", "2.1.9", true},
		{"garbage", "1.0.0", false},
	}
	for _, c := range cases {
		if VersionAtLeast(c.have, c.want) != c.ok {
			t.Errorf("%s >= %s should be %v", c.have, c.want, c.ok)
		}
	}
}

func TestShellQuote(t *testing.T) {
	if shellQuote("/Users/me/Code/x") != "/Users/me/Code/x" {
		t.Error("plain path should not be quoted")
	}
	if got := shellQuote("it's here"); got != `'it'\''s here'` {
		t.Errorf("got %s", got)
	}
	if shellQuote("") != "''" {
		t.Error("empty")
	}
}

func TestTested(t *testing.T) {
	for v, want := range map[string]bool{"2.2.0": true, "2.2.5": true, "limactl version 2.2.0": true, "2.3.0": false, "1.0.0": false, "": false} {
		if got := Tested(v); got != want {
			t.Errorf("Tested(%q) = %v", v, got)
		}
	}
}

// Run must return the guest command's stdout only: Lima 2.x writes
// `level=warning` lines to stderr on every `shell`, and a probe comparing the
// result to "active" saw them prepended (every control shown as failed).
func TestRunReturnsStdoutOnly(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "limactl")
	script := "#!/bin/sh\necho 'time=\"x\" level=warning msg=\"provisioning scripts should not reference the LIMA_CIDATA variables\"' >&2\n" +
		"case \"$*\" in *fail*) echo 'guest said no' >&2; exit 3;; esac\necho active\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil { //nolint:gosec // test helper must be executable
		t.Fatal(err)
	}
	c := &Client{Bin: fake, LimaHome: dir}
	out, err := c.Run(t.Context(), "box", "systemctl", "is-active", "x")
	if err != nil || out != "active\n" {
		t.Fatalf("want stdout only, got %q err %v", out, err)
	}
	_, err = c.Run(t.Context(), "box", "fail")
	if err == nil || !strings.Contains(err.Error(), "guest said no") || strings.Contains(err.Error(), "level=warning") {
		t.Fatalf("error must carry the guest's stderr without Lima's log lines: %v", err)
	}
}
