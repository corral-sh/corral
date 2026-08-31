package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/corral-sh/corral/internal/config"
	"github.com/corral-sh/corral/internal/paths"
)

func TestMergePrecedenceAndUnion(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "config.toml")
	project := filepath.Join(dir, "proj")
	_ = os.MkdirAll(project, 0o755)
	if err := os.WriteFile(global, []byte(`
cpus = 6
memory = "12GiB"
packages = ["a"]
toolchains = ["node", "go"]
git_tokens = { "git.example.com" = "TOKEN_A", "github.com" = "GH" }
mounts = ["`+dir+`:/shared:ro"]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, config.ProjectFileName), []byte(`
cpus = 2
packages = ["b", "a"]
env = ["DEBUG=1"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(global, "", project)
	if err != nil {
		t.Fatal(err)
	}
	if c.CPUs != 2 {
		t.Errorf("project should win: cpus=%d", c.CPUs)
	}
	if c.Memory != "12GiB" {
		t.Errorf("global should apply when project silent: %s", c.Memory)
	}
	if strings.Join(c.Packages, ",") != "a,b" {
		t.Errorf("packages union+sort: %v", c.Packages)
	}
	if strings.Join(c.Toolchains, ",") != "node,go" {
		t.Errorf("toolchains: %v", c.Toolchains)
	}
	if c.GitTokens["git.example.com"].Token != "TOKEN_A" || c.GitTokens["github.com"].Token != "GH" {
		t.Errorf("git_tokens from global: %v", c.GitTokens)
	}
	if len(c.Mounts) != 1 || c.Mounts[0].Writable || c.Mounts[0].Guest != "/shared" {
		t.Errorf("mounts: %+v", c.Mounts)
	}
	if len(c.Sources) != 2 {
		t.Errorf("sources: %v", c.Sources)
	}
}

func TestUnknownKeyRejected(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	_ = os.WriteFile(p, []byte("cpu = 4\n"), 0o600)
	if _, err := Load(p, "", ""); err == nil || !strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("expected unknown key error, got %v", err)
	}
}

func TestWriteGlobalRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "config.toml")
	f := config.File{CPUs: ptr(3), Toolchains: []string{"go"}, GitTokens: map[string]config.GitToken{"h.example": {Token: "V"}}}
	if err := config.WriteGlobal(p, f); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if c.CPUs != 3 || c.GitTokens["h.example"].Token != "V" {
		t.Errorf("round trip: %+v", c)
	}
}

func ptr[T any](v T) *T { return &v }

func TestProjectPathRefusals(t *testing.T) {
	home, _ := os.UserHomeDir()
	for _, p := range []string{"/", home, "/usr/local", "/etc"} {
		if err := ProjectPath(p); err == nil {
			t.Errorf("%s should be refused as a project", p)
		}
	}
	if err := ProjectPath(t.TempDir()); err != nil {
		t.Errorf("temp dir should be allowed: %v", err)
	}
	// --repo placeholders under ~/.corral/repos/<slug> are the one allowed
	// state path (never mounted); anything else under ~/.corral is refused.
	t.Setenv("CORRAL_HOME", filepath.Join(t.TempDir(), "eb"))
	h, _ := paths.Home()
	if err := ProjectPath(filepath.Join(h, "repos", "git-host-g-p")); err != nil {
		t.Errorf("repo placeholder must be allowed: %v", err)
	}
	for _, bad := range []string{h, filepath.Join(h, "repos"), filepath.Join(h, "boxes", "x"), filepath.Join(h, "repos", "a", "b")} {
		if err := ProjectPath(bad); err == nil {
			t.Errorf("%s must be refused", bad)
		}
	}
}

func TestHidePath(t *testing.T) {
	for _, bad := range []string{"", "/etc/passwd", "..", "../other", "a/../..", ".", ".git", ".git/config"} {
		if _, err := HidePath(bad); err == nil {
			t.Errorf("hide %q should be refused", bad)
		}
	}
	for in, want := range map[string]string{".env": ".env", "secrets/": "secrets/", "./a/b": "a/b", "a/../b/": "b/"} {
		got, err := HidePath(in)
		if err != nil || got != want {
			t.Errorf("hide %q: got %q, %v; want %q", in, got, err, want)
		}
	}
}

// The host per-project file is the user's: it may grant what the repo cannot,
// and it wins over the project file.
func TestHostProjectLayerIsTrustedAndWins(t *testing.T) {
	gp, pd := writeProject(t, "cpus = 2\nreadonly_project = true\n")
	hp := filepath.Join(filepath.Dir(gp), "projects", "proj-abc123.toml")
	_ = os.MkdirAll(filepath.Dir(hp), 0o700)
	_ = os.WriteFile(hp, []byte(`
ssh_agent = true
mounts = ["/tmp:/hosttmp:ro"]
git_tokens = { "git.example.com" = "TOK" }
readonly_project = false
`), 0o600)
	c, err := Load(gp, hp, pd)
	if err != nil {
		t.Fatalf("host layer must load: %v", err)
	}
	if !c.SSHAgent || len(c.Mounts) != 1 || c.GitTokens["git.example.com"].Token != "TOK" {
		t.Errorf("host layer not applied: %+v", c)
	}
	if c.ReadonlyProject {
		t.Error("host layer must win over the project file")
	}
	if c.CPUs != 2 {
		t.Error("project layer must still apply where the host file is silent")
	}
	if len(c.Sources) != 2 {
		t.Errorf("sources: %v", c.Sources)
	}
}

func TestBoxDirPath(t *testing.T) {
	for _, bad := range []string{"", "/abs", "..", "../x", ".", ".git", ".git/objects"} {
		if _, err := BoxDirPath(bad); err == nil {
			t.Errorf("BoxDirPath(%q) must be refused", bad)
		}
	}
	for in, want := range map[string]string{"node_modules": "node_modules", "build/": "build", "./a/b": "a/b"} {
		got, err := BoxDirPath(in)
		if err != nil || got != want {
			t.Errorf("BoxDirPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}
