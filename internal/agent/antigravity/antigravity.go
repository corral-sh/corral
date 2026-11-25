// Package antigravity implements Google's Antigravity CLI (`agy`) agent, the
// terminal agent that replaced Gemini CLI.
package antigravity

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/corral-sh/corral/internal/agent"
)

// Antigravity is Google's Antigravity CLI.
type Antigravity struct{}

func init() { agent.Register(Antigravity{}) }

// Name implements agent.Agent.
func (Antigravity) Name() string { return "antigravity" }

// Summary implements agent.Agent.
func (Antigravity) Summary() string {
	return "Antigravity CLI (Google, `agy`) — vendor installer, sha512-verified against Google's release manifest"
}

// installerURL is the documented installer. It is the vendor's own installer,
// the one `curl | bash` exception the guest-script rule allows: it reads a
// per-platform manifest from Google's release server and checks the sha512
// it lists before installing ~/.local/bin/agy (no npm, no Node).
const installerURL = "https://antigravity.google/cli/install.sh"

// ProvisionScript installs the Antigravity CLI. The installer exits early
// when the binary exists, so a re-run on every boot is cheap.
func (Antigravity) ProvisionScript() string {
	return `#!/bin/bash
set -euo pipefail
bin="$HOME/.local/bin/agy"
if [ -x "$bin" ]; then
  echo "[corral] antigravity already installed: $("$bin" --version 2>/dev/null || true)"
  exit 0
fi
echo "[corral] installing Antigravity CLI from antigravity.google"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
curl -fsSL -o "$tmp/install.sh" ` + installerURL + `
bash "$tmp/install.sh"
"$bin" --version
`
}

// stateLinks are the fixed home paths Antigravity keeps its state in, and the
// sub-directory of the agent state dir each one points to. The CLI has no
// variable that relocates them (unlike CLAUDE_CONFIG_DIR or CODEX_HOME), so
// the seed script makes them symlinks into the state dir: the login, settings
// and history then survive rebuilds and follow agent_state like the others.
var stateLinks = map[string]string{
	".gemini":             "gemini",
	".config/antigravity": "config",
}

// SeedStateScript implements agent.StateSeeder: it points the home paths at
// the state dir. A real directory already in their place (an earlier run in
// this box) is merged into the state dir without overwriting, then replaced
// by the link. Idempotent, and it never touches an existing login.
func (Antigravity) SeedStateScript(stateDir string) string {
	var b strings.Builder
	b.WriteString("#!/bin/bash\nset -euo pipefail\n")
	b.WriteString("state=" + shellQuote(stateDir) + "\n")
	b.WriteString("[ -d \"$state\" ] || exit 0\n")
	for _, home := range sortedKeys(stateLinks) {
		sub := stateLinks[home]
		fmt.Fprintf(&b, `mkdir -p "$state/%[2]s" "$(dirname "$HOME/%[1]s")"
if [ -d "$HOME/%[1]s" ] && [ ! -L "$HOME/%[1]s" ]; then
  cp -an "$HOME/%[1]s/." "$state/%[2]s/" && rm -rf "$HOME/%[1]s"
fi
ln -sfn "$state/%[2]s" "$HOME/%[1]s"
`, home, sub)
	}
	return b.String()
}

// GuestEnv turns off the background self-update: the box's agy is provisioned
// by Corral (rebuild updates it), and in broker mode the updater's host would
// only add denials to the egress log. Only the value "true" works — "1" is
// ignored (measured on agy 1.5.16).
func (Antigravity) GuestEnv(string) map[string]string {
	return map[string]string{"AGY_CLI_DISABLE_AUTO_UPDATE": "true"}
}

// ForwardEnv implements agent.Agent. API-key auth is not forwarded until it is
// verified to work without a Google login.
func (Antigravity) ForwardEnv() []string { return nil }

// Binary implements agent.Agent.
func (Antigravity) Binary() string { return "agy" }

// YoloArgs implements agent.Agent. Antigravity's own --sandbox is opt-in and
// left off: the box is the sandbox.
func (Antigravity) YoloArgs() []string { return []string{"--dangerously-skip-permissions"} }

// subcommands start no agent session (agy 1.5.16 --help); they get no
// permission flag.
var subcommands = map[string]bool{
	"acp": true, "agent": true, "agents": true, "changelog": true, "help": true, "install": true,
	"mcp": true, "mic-serve": true, "models": true, "plugin": true, "plugins": true,
	"remote-control": true, "update": true,
}

// Argv implements agent.Agent.
func (a Antigravity) Argv(opts agent.LaunchOptions) []string {
	argv := []string{a.Binary()}
	if len(opts.Args) > 0 && subcommands[opts.Args[0]] {
		return append(argv, opts.Args...)
	}
	if opts.Yolo && !hasPermissionFlag(opts.Args) {
		argv = append(argv, a.YoloArgs()...)
	}
	return append(argv, opts.Args...)
}

// hasPermissionFlag: the user's own approval choice wins.
func hasPermissionFlag(args []string) bool {
	for _, x := range args {
		name, _, _ := strings.Cut(x, "=")
		if name == "--dangerously-skip-permissions" || name == "--mode" {
			return true
		}
	}
	return false
}

// VersionArgv implements agent.Agent.
func (Antigravity) VersionArgv() []string { return []string{"agy", "--version"} }

// HostConfigImports lists the non-secret pieces of ~/.gemini worth copying
// into the box on request. Tokens and plugin secrets are deliberately absent.
func (Antigravity) HostConfigImports() map[string]string {
	return map[string]string{
		".gemini/GEMINI.md":                     "gemini/GEMINI.md",
		".gemini/config/skills":                 "gemini/config/skills",
		".gemini/config/workflows":              "gemini/config/workflows",
		".gemini/antigravity-cli/settings.json": "gemini/antigravity-cli/settings.json",
	}
}

// LoginHint implements agent.Agent.
func (Antigravity) LoginHint() string {
	return "Run `agy` inside the box once: open the URL it prints on your Mac, sign in with Google and paste the code back; the login is stored in ~/.corral/agents/antigravity and reused by every box."
}

// EgressHosts implements agent.Agent. Measured on agy 1.5.16 in a broker box
// (2026-11-25): a Google login plus a prompt used oauth2 (code exchange and
// token refresh), www (user info), daily-cloudcode-pa (the model backend) and
// aicode. cloudcode-pa and businessaicode are the production siblings the
// binary selects by account tier; that login did not exercise them. Left out
// on purpose: play.googleapis.com (usage logging, no opt-out found — it shows
// as recurring denials in `corral egress`), lh3.googleusercontent.com (profile
// picture) and www.gstatic.com; the agent works without them.
func (Antigravity) EgressHosts() []string {
	return []string{
		"oauth2.googleapis.com", "www.googleapis.com",
		"daily-cloudcode-pa.googleapis.com", "cloudcode-pa.googleapis.com",
		"aicode.googleapis.com", "businessaicode.googleapis.com",
	}
}

// tokenFile is where agy keeps its Google login when it runs over SSH (every
// Corral session does): file-based storage instead of a keyring.
const tokenFile = "gemini/antigravity-cli/antigravity-oauth-token"

// LoggedIn implements agent.Agent.
func (Antigravity) LoggedIn(stateDir string, _ func(string) string) bool {
	_, err := os.Stat(filepath.Join(stateDir, tokenFile))
	return err == nil
}

// Aliases implements agent.Aliaser: `corral agy`, the binary's own name.
func (Antigravity) Aliases() []string { return []string{"agy"} }

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
