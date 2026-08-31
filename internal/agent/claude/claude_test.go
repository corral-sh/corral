package claude

import (
	"strings"
	"testing"

	"github.com/corral-sh/corral/internal/agent"
)

func TestRegistered(t *testing.T) {
	a, ok := agent.Lookup("claude")
	if !ok || a.Name() != "claude" || a.Binary() != "claude" {
		t.Fatal("claude not registered")
	}
}

func TestArgv(t *testing.T) {
	c := Claude{}
	got := strings.Join(c.Argv(agent.LaunchOptions{Yolo: true, Args: []string{"-p", "x"}}), " ")
	if got != "claude --dangerously-skip-permissions -p x" {
		t.Errorf("yolo argv: %s", got)
	}
	got = strings.Join(c.Argv(agent.LaunchOptions{Yolo: false}), " ")
	if got != "claude" {
		t.Errorf("ask argv: %s", got)
	}
	// User-provided permission flag wins; do not double up.
	got = strings.Join(c.Argv(agent.LaunchOptions{Yolo: true, Args: []string{"--permission-mode", "plan"}}), " ")
	if strings.Contains(got, "dangerously") {
		t.Errorf("should not add yolo flag: %s", got)
	}
}

func TestNoCredentialsImported(t *testing.T) {
	for src := range (Claude{}).HostConfigImports() {
		if strings.Contains(src, "credential") || strings.Contains(src, ".claude.json") {
			t.Errorf("%s must never be imported", src)
		}
	}
}

func TestGuestEnvRelocatesConfig(t *testing.T) {
	env := (Claude{}).GuestEnv("/corral/agents/claude")
	if env["DISABLE_AUTOUPDATER"] != "1" {
		t.Fatalf("DISABLE_AUTOUPDATER = %q, want 1 (the box's claude is provisioned, not self-updated)", env["DISABLE_AUTOUPDATER"])
	}
	if env["CLAUDE_CONFIG_DIR"] != "/corral/agents/claude" {
		t.Errorf("%v", env)
	}
}

func TestSeedStateScriptIsIdempotent(t *testing.T) {
	var a agent.Agent = Claude{}
	sd, ok := a.(agent.StateSeeder)
	if !ok {
		t.Fatal("claude must seed its onboarding flag")
	}
	s := sd.SeedStateScript("/corral/agents/claude")
	for _, want := range []string{`[ ! -e '/corral/agents/claude'/.claude.json ]`, `"hasCompletedOnboarding": true`, "set -euo pipefail"} {
		if !strings.Contains(s, want) {
			t.Errorf("seed script missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "credential") || strings.Contains(s, "oauth") {
		t.Errorf("seed must never write credentials:\n%s", s)
	}
}
