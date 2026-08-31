package box

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteSSHInclude(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ssh", "config")
	if err := WriteSSHInclude(p, "/lima home", []string{"b", "a"}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	want := "Include \"/lima home/a/ssh.config\"\nInclude \"/lima home/b/ssh.config\"\n"
	if !strings.HasSuffix(string(got), want) {
		t.Fatalf("got:\n%s", got)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
}

func TestEnsureUserSSHInclude(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, ".ssh", "config")
	inc := filepath.Join(dir, "eb", "config")

	// Missing file: created with the include.
	changed, err := EnsureUserSSHInclude(cfg, inc)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	// Idempotent.
	changed, err = EnsureUserSSHInclude(cfg, inc)
	if err != nil || changed {
		t.Fatalf("second run changed=%v err=%v", changed, err)
	}

	// Existing content is preserved below the include.
	body := "Host work\n  HostName example.com\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureUserSSHInclude(cfg, inc); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(cfg)
	s := string(got)
	if !strings.HasSuffix(s, body) {
		t.Fatalf("original config not preserved:\n%s", s)
	}
	if strings.Index(s, "Include") > strings.Index(s, "Host work") {
		t.Fatalf("include must come before the first Host block:\n%s", s)
	}

	// Unquoted, differently-cased existing include is recognised.
	if err := os.WriteFile(cfg, []byte("include "+inc+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	has, err := HasUserSSHInclude(cfg, inc)
	if err != nil || !has {
		t.Fatalf("has=%v err=%v", has, err)
	}
}
