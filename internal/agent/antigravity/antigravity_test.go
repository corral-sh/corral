package antigravity

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/corral-sh/corral/internal/agent"
)

func TestRegistered(t *testing.T) {
	a, ok := agent.Lookup("antigravity")
	if !ok || a.Name() != "antigravity" || a.Binary() != "agy" {
		t.Fatal("antigravity not registered")
	}
	if al, ok := a.(agent.Aliaser); !ok || strings.Join(al.Aliases(), ",") != "agy" {
		t.Error("`corral agy` alias missing")
	}
}

func TestArgv(t *testing.T) {
	a := Antigravity{}
	const yolo = "--dangerously-skip-permissions"
	for _, tc := range []struct {
		yolo bool
		args []string
		want string
	}{
		{true, nil, "agy " + yolo},
		{true, []string{"-p", "fix the tests"}, "agy " + yolo + " -p fix the tests"},
		{false, []string{"-p", "x"}, "agy -p x"},
		// Sub-commands start no session: nothing added.
		{true, []string{"mcp", "list"}, "agy mcp list"},
		{true, []string{"models"}, "agy models"},
		// The user's own permission choice wins.
		{true, []string{"--mode", "plan"}, "agy --mode plan"},
		{true, []string{"--mode=accept-edits", "x"}, "agy --mode=accept-edits x"},
	} {
		if got := strings.Join(a.Argv(agent.LaunchOptions{Yolo: tc.yolo, Args: tc.args}), " "); got != tc.want {
			t.Errorf("Argv(yolo=%v, %q) = %q, want %q", tc.yolo, tc.args, got, tc.want)
		}
	}
}

// Only "true" disables agy's self-update; "1" is ignored (measured).
func TestGuestEnvDisablesAutoUpdate(t *testing.T) {
	if v := (Antigravity{}).GuestEnv("/x")["AGY_CLI_DISABLE_AUTO_UPDATE"]; v != "true" {
		t.Errorf("AGY_CLI_DISABLE_AUTO_UPDATE = %q, want \"true\"", v)
	}
}

func TestNoCredentialsImported(t *testing.T) {
	for src, dst := range (Antigravity{}).HostConfigImports() {
		for _, bad := range []string{"token", "secret", "credential", "oauth"} {
			if strings.Contains(src, bad) || strings.Contains(dst, bad) {
				t.Errorf("%s must never be imported", src)
			}
		}
	}
}

// Guest-script rule: the vendor's own installer is the one curl | bash
// exception, and it is downloaded to a file first, not piped.
func TestProvisionScript(t *testing.T) {
	s := (Antigravity{}).ProvisionScript()
	for _, want := range []string{"set -euo pipefail", installerURL, `[ -x "$bin" ]`} {
		if !strings.Contains(s, want) {
			t.Errorf("provision script lacks %q", want)
		}
	}
	if strings.Contains(s, "| bash") || strings.Contains(s, "| sh") {
		t.Error("provision script must not pipe a download into a shell")
	}
}

func TestEgressHosts(t *testing.T) {
	got := strings.Join((Antigravity{}).EgressHosts(), " ")
	for _, h := range []string{"oauth2.googleapis.com", "daily-cloudcode-pa.googleapis.com", "aicode.googleapis.com"} {
		if !strings.Contains(got, h) {
			t.Errorf("egress lacks %s: %s", h, got)
		}
	}
	for _, h := range []string{"play.googleapis.com", "*.", "sandbox"} {
		if strings.Contains(got, h) {
			t.Errorf("egress must not include %q: %s", h, got)
		}
	}
}

func TestLoggedIn(t *testing.T) {
	dir := t.TempDir()
	none := func(string) string { return "" }
	if (Antigravity{}).LoggedIn(dir, none) {
		t.Error("empty state dir: not logged in")
	}
	p := filepath.Join(dir, tokenFile)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !(Antigravity{}).LoggedIn(dir, none) {
		t.Error("token file counts")
	}
}

// The seed script links the fixed home paths into the state dir, merges a
// real directory already there, and is idempotent.
func TestSeedStateScriptLinksHomeIntoState(t *testing.T) {
	home, state := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".gemini"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gemini", "earlier"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func() {
		cmd := exec.CommandContext(t.Context(), "bash", "-c", (Antigravity{}).SeedStateScript(state))
		cmd.Env = append(os.Environ(), "HOME="+home)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("seed: %v\n%s", err, out)
		}
	}
	run()
	run() // idempotent
	for home2, sub := range stateLinks {
		target, err := os.Readlink(filepath.Join(home, home2))
		if err != nil || target != filepath.Join(state, sub) {
			t.Errorf("~/%s -> %q (%v), want %s", home2, target, err, filepath.Join(state, sub))
		}
	}
	if b, err := os.ReadFile(filepath.Join(state, "gemini", "earlier")); err != nil || string(b) != "keep" {
		t.Errorf("an existing ~/.gemini must be merged into the state dir: %q %v", b, err)
	}
}
