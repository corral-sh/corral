// Package claude implements the Claude Code agent for Corral.
package claude

import (
	"strings"

	"github.com/corral-sh/corral/internal/agent"
)

// Claude is the Claude Code agent.
type Claude struct{}

func init() { agent.Register(Claude{}) }

// Name implements agent.Agent.
func (Claude) Name() string { return "claude" }

// Summary implements agent.Agent.
func (Claude) Summary() string {
	return "Claude Code (Anthropic) — installed from the official installer"
}

// ProvisionScript installs Claude Code with Anthropic's native installer.
// No npm, no third-party image: the only remote we trust is claude.ai.
func (Claude) ProvisionScript() string {
	return `#!/bin/bash
set -euo pipefail
if [ -x "$HOME/.local/bin/claude" ]; then
  echo "[corral] claude already installed: $("$HOME/.local/bin/claude" --version 2>/dev/null || true)"
  exit 0
fi
echo "[corral] installing Claude Code from https://claude.ai/install.sh"
curl -fsSL https://claude.ai/install.sh | bash
"$HOME/.local/bin/claude" --version
`
}

// GuestEnv relocates all Claude Code state (settings, history, credentials,
// ~/.claude.json) into the mounted state directory so a login survives box
// rebuilds and is shared between boxes.
func (Claude) GuestEnv(stateDir string) map[string]string {
	return map[string]string{
		"CLAUDE_CONFIG_DIR": stateDir,
		// The box's Claude Code is provisioned by Corral (rebuild updates
		// it); self-update inside the VM is pointless and, in broker/offline
		// mode, fails at every start with "Auto-update failed".
		"DISABLE_AUTOUPDATER": "1",
	}
}

// SeedStateScript implements agent.StateSeeder. Claude Code shows its
// onboarding wizard ("Select login method", theme) while ~/.claude.json —
// relocated into the state dir by CLAUDE_CONFIG_DIR — lacks
// hasCompletedOnboarding, even when CLAUDE_CODE_OAUTH_TOKEN / ANTHROPIC_API_KEY
// is set, so a forwarded token was never used. A user who reached the
// box has already authenticated somewhere; the wizard is noise. Only a
// missing file is created: an existing login is never touched.
func (Claude) SeedStateScript(stateDir string) string {
	q := shellQuote(stateDir)
	return "#!/bin/bash\nset -euo pipefail\n" +
		"if [ -d " + q + " ] && [ ! -e " + q + "/.claude.json ]; then\n" +
		"  printf '{\"hasCompletedOnboarding\": true}\\n' >" + q + "/.claude.json\n" +
		"  echo \"[corral] claude: seeded onboarding flag (a forwarded token is used directly)\"\n" +
		"fi\n"
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// ForwardEnv lists the host variables that authenticate Claude Code.
func (Claude) ForwardEnv() []string {
	return []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"}
}

// Binary implements agent.Agent.
func (Claude) Binary() string { return "claude" }

// YoloArgs implements agent.Agent.
func (Claude) YoloArgs() []string { return []string{"--dangerously-skip-permissions"} }

// Argv implements agent.Agent. The wrapper in /opt/corral/bin is bypassed
// by disabling CORRAL_YOLO; the decision is made here, once.
func (c Claude) Argv(opts agent.LaunchOptions) []string {
	argv := []string{c.Binary()}
	if opts.Yolo && !hasPermissionFlag(opts.Args) {
		argv = append(argv, c.YoloArgs()...)
	}
	return append(argv, opts.Args...)
}

func hasPermissionFlag(args []string) bool {
	for _, a := range args {
		switch a {
		case "--dangerously-skip-permissions", "--permission-mode", "--allowedTools", "--allowed-tools":
			return true
		}
	}
	return false
}

// VersionArgv implements agent.Agent.
func (Claude) VersionArgv() []string { return []string{"claude", "--version"} }

// HostConfigImports lists the non-secret pieces of ~/.claude worth copying
// into the box on request. Credentials (.credentials.json, Keychain) are
// deliberately absent.
func (Claude) HostConfigImports() map[string]string {
	return map[string]string{
		".claude/CLAUDE.md":     "CLAUDE.md",
		".claude/settings.json": "settings.json",
		".claude/skills":        "skills",
		".claude/agents":        "agents",
		".claude/commands":      "commands",
	}
}

// LoginHint implements agent.Agent.
func (Claude) LoginHint() string {
	return "Run `/login` inside Claude once; the session is stored in ~/.corral/agents/claude and reused by every box. " +
		"Alternatively export ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN (from `claude setup-token`) on the host."
}
