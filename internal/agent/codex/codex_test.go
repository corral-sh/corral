package codex

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/corral-sh/corral/internal/agent"
)

func TestRegistered(t *testing.T) {
	a, ok := agent.Lookup("codex")
	if !ok || a.Name() != "codex" || a.Binary() != "codex" {
		t.Fatal("codex not registered")
	}
}

func TestArgv(t *testing.T) {
	c := Codex{}
	const nu = "-c check_for_update_on_startup=false "
	const bypass = "--dangerously-bypass-approvals-and-sandbox"
	for _, tc := range []struct {
		yolo bool
		args []string
		want string
	}{
		{true, nil, "codex " + nu + bypass},
		{true, []string{"fix the tests"}, "codex " + nu + bypass + " fix the tests"},
		{true, []string{"-m", "gpt-5", "hi"}, "codex " + nu + bypass + " -m gpt-5 hi"},
		// The flag belongs to the session sub-command, after its name.
		{true, []string{"exec", "summarise this repo"}, "codex exec " + nu + bypass + " summarise this repo"},
		{true, []string{"resume", "--last"}, "codex resume " + nu + bypass + " --last"},
		// Sub-commands that start no session get nothing added; review rejects the flag.
		{true, []string{"login", "--device-auth"}, "codex login --device-auth"},
		{true, []string{"review"}, "codex review"},
		{true, []string{"mcp", "list"}, "codex mcp list"},
		// The user's own approval/sandbox choice wins.
		{true, []string{"--sandbox", "read-only"}, "codex " + nu + "--sandbox read-only"},
		{true, []string{"exec", "-a=never", "x"}, "codex exec " + nu + "-a=never x"},
		// --ask keeps the prompts; the in-box sandbox cannot run (AppArmor).
		{false, []string{"exec", "x"}, "codex exec " + nu + "--sandbox danger-full-access x"},
		{false, nil, "codex " + nu + "--sandbox danger-full-access"},
	} {
		if got := strings.Join(c.Argv(agent.LaunchOptions{Yolo: tc.yolo, Args: tc.args}), " "); got != tc.want {
			t.Errorf("Argv(yolo=%v, %q) = %q, want %q", tc.yolo, tc.args, got, tc.want)
		}
	}
}

func TestNoCredentialsImported(t *testing.T) {
	for src, dst := range (Codex{}).HostConfigImports() {
		if strings.Contains(src, "auth") || strings.Contains(dst, "auth") {
			t.Errorf("%s must never be imported", src)
		}
	}
}

func TestGuestEnvRelocatesState(t *testing.T) {
	if env := (Codex{}).GuestEnv("/corral/agents/codex"); env["CODEX_HOME"] != "/corral/agents/codex" {
		t.Errorf("%v", env)
	}
}

// Guest-script rule: download + checksum-verify, never curl | bash.
func TestProvisionScriptVerifiesChecksum(t *testing.T) {
	s := (Codex{}).ProvisionScript()
	for _, want := range []string{"set -euo pipefail", "sha256sum -c -", ".digest", "refusing to install", "https://github.com/openai/codex/releases/download/"} {
		if !strings.Contains(s, want) {
			t.Errorf("provision script lacks %q", want)
		}
	}
	if regexp.MustCompile(`\|\s*(ba)?sh\b`).MatchString(s) {
		t.Error("provision script must not pipe a download into a shell")
	}
}

func TestEgressHosts(t *testing.T) {
	got := strings.Join((Codex{}).EgressHosts(), " ")
	for _, h := range []string{"api.openai.com", "auth.openai.com", "chatgpt.com"} {
		if !strings.Contains(got, h) {
			t.Errorf("egress lacks %s: %s", h, got)
		}
	}
}

func TestLoggedIn(t *testing.T) {
	dir := t.TempDir()
	none := func(string) string { return "" }
	if (Codex{}).LoggedIn(dir, none) {
		t.Error("empty state dir, no key: not logged in")
	}
	if !(Codex{}).LoggedIn(dir, func(k string) string {
		if k == "OPENAI_API_KEY" {
			return "sk-x"
		}
		return ""
	}) {
		t.Error("OPENAI_API_KEY on the host counts")
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !(Codex{}).LoggedIn(dir, none) {
		t.Error("auth.json counts")
	}
}
