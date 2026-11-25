// Package codex implements the OpenAI Codex CLI agent for Corral.
package codex

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/corral-sh/corral/internal/agent"
)

// Codex is the OpenAI Codex CLI.
type Codex struct{}

func init() { agent.Register(Codex{}) }

// Name implements agent.Agent.
func (Codex) Name() string { return "codex" }

// Summary implements agent.Agent.
func (Codex) Summary() string {
	return "Codex CLI (OpenAI) — release binary from github.com/openai/codex, sha256-verified"
}

// ProvisionScript installs the newest Codex CLI release binary (a static musl
// build — no npm, no Node). The checksum is the sha256 digest GitHub records
// for the release asset; like nodejs.org's SHASUMS256.txt it comes from the
// same origin as the download, so it catches a corrupt or swapped CDN object,
// not a compromised release.
func (Codex) ProvisionScript() string {
	return `#!/bin/bash
set -euo pipefail
bin="$HOME/.local/bin/codex"
if [ -x "$bin" ]; then
  echo "[corral] codex already installed: $("$bin" --version 2>/dev/null || true)"
  exit 0
fi
case "$(uname -m)" in
  aarch64) triple=aarch64-unknown-linux-musl ;;
  x86_64) triple=x86_64-unknown-linux-musl ;;
  *) echo "corral: codex has no release for $(uname -m)" >&2; exit 1 ;;
esac
asset="codex-$triple.tar.gz"
rel=$(curl -fsSL -H 'Accept: application/vnd.github+json' https://api.github.com/repos/openai/codex/releases/latest)
tag=$(jq -r '.tag_name' <<<"$rel")
digest=$(jq -r --arg a "$asset" '.assets[] | select(.name == $a) | .digest' <<<"$rel")
case "$digest" in
  sha256:*) ;;
  *) echo "corral: no sha256 digest for $asset in codex release $tag — refusing to install" >&2; exit 1 ;;
esac
echo "[corral] installing Codex CLI $tag from github.com/openai/codex"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
curl -fsSL -o "$tmp/$asset" "https://github.com/openai/codex/releases/download/$tag/$asset"
echo "${digest#sha256:}  $tmp/$asset" | sha256sum -c -
tar -xzf "$tmp/$asset" -C "$tmp"
mkdir -p "$HOME/.local/bin"
install -m 0755 "$tmp/codex-$triple" "$bin"
"$bin" --version
`
}

// GuestEnv relocates all Codex state (config.toml, auth.json, sessions,
// history) into the mounted state directory, so a login survives rebuilds and
// is shared between boxes like Claude's.
func (Codex) GuestEnv(stateDir string) map[string]string {
	return map[string]string{"CODEX_HOME": stateDir}
}

// ForwardEnv lists the host variables that authenticate Codex. CODEX_API_KEY
// is what `codex exec` reads for a one-shot run; OPENAI_API_KEY is what
// `codex login --with-api-key` is fed from.
func (Codex) ForwardEnv() []string { return []string{"OPENAI_API_KEY", "CODEX_API_KEY"} }

// Binary implements agent.Agent.
func (Codex) Binary() string { return "codex" }

// YoloArgs turns off Codex's approval prompts and its own Linux sandbox: the
// box is the boundary.
func (Codex) YoloArgs() []string { return []string{"--dangerously-bypass-approvals-and-sandbox"} }

// askArgs keep the approval prompts (`--ask`) but not Codex's own sandbox: it
// needs unprivileged user namespaces, which Ubuntu 24.04's AppArmor denies in
// the box ("bwrap: loopback: Failed RTM_NEWADDR"), so every command would
// fail. The box is the sandbox either way.
var askArgs = []string{"--sandbox", "danger-full-access"}

// noUpdateCheck: the box's Codex is provisioned by Corral (rebuild updates
// it), like Claude's DISABLE_AUTOUPDATER; in broker mode the check would only
// add denied github.com lines to the egress log.
var noUpdateCheck = []string{"-c", "check_for_update_on_startup=false"}

// Argv implements agent.Agent. The permission flags are per-command options,
// so they go after a session sub-command (`codex exec …`, `codex resume …`);
// the sub-commands that start no session (login, mcp, review …) are passed
// through untouched — `codex review` rejects the bypass flag.
func (c Codex) Argv(opts agent.LaunchOptions) []string {
	argv := []string{c.Binary()}
	args := opts.Args
	if len(args) > 0 {
		if sessionSubcommands[args[0]] {
			argv, args = append(argv, args[0]), args[1:]
		} else if otherSubcommands[args[0]] {
			return append(argv, args...)
		}
	}
	argv = append(argv, noUpdateCheck...)
	if !hasPermissionFlag(args) {
		if opts.Yolo {
			argv = append(argv, c.YoloArgs()...)
		} else {
			argv = append(argv, askArgs...)
		}
	}
	return append(argv, args...)
}

// sessionSubcommands run the agent and accept the approval/sandbox flags.
var sessionSubcommands = map[string]bool{"exec": true, "e": true, "resume": true, "fork": true}

// otherSubcommands start no agent session of their own (codex 0.159 `--help`).
var otherSubcommands = map[string]bool{
	"agents": true, "review": true, "login": true, "logout": true, "mcp": true, "plugin": true,
	"app-server": true, "remote-control": true, "completion": true, "update": true, "doctor": true,
	"sandbox": true, "debug": true, "apply": true, "a": true, "queue": true, "archive": true,
	"delete": true, "migrate-rollouts": true, "unarchive": true, "cloud": true, "exec-server": true,
	"features": true, "help": true, "mcp-server": true,
}

func hasPermissionFlag(args []string) bool {
	for _, a := range args {
		name, _, _ := strings.Cut(a, "=")
		switch name {
		case "--dangerously-bypass-approvals-and-sandbox", "--yolo", "--full-auto", "--approve-for-me",
			"-a", "--ask-for-approval", "-s", "--sandbox":
			return true
		}
	}
	return false
}

// VersionArgv implements agent.Agent.
func (Codex) VersionArgv() []string { return []string{"codex", "--version"} }

// HostConfigImports lists the non-secret pieces of ~/.codex worth copying into
// the box on request. auth.json (the login) is deliberately absent.
func (Codex) HostConfigImports() map[string]string {
	return map[string]string{
		".codex/config.toml": "config.toml",
		".codex/AGENTS.md":   "AGENTS.md",
		".codex/prompts":     "prompts",
		".codex/skills":      "skills",
	}
}

// LoginHint implements agent.Agent.
func (Codex) LoginHint() string {
	return "Run `codex login --device-auth` inside the box once (ChatGPT sign-in with a code, no browser callback); the login is stored in ~/.corral/agents/codex and reused by every box. " +
		"With an API key instead: export OPENAI_API_KEY on the host and run `printenv OPENAI_API_KEY | codex login --with-api-key` in the box."
}

// EgressHosts implements agent.Agent: the Responses API (API-key logins),
// the ChatGPT backend (ChatGPT-plan logins) and the OAuth endpoints that sign
// in and refresh the token.
func (Codex) EgressHosts() []string {
	return []string{"api.openai.com", "auth.openai.com", "chatgpt.com"}
}

// LoggedIn implements agent.Agent.
func (c Codex) LoggedIn(stateDir string, getenv func(string) string) bool {
	if _, err := os.Stat(filepath.Join(stateDir, "auth.json")); err == nil {
		return true
	}
	for _, k := range c.ForwardEnv() {
		if getenv(k) != "" {
			return true
		}
	}
	return false
}
