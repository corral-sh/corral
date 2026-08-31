// Package agent defines the extension point for AI coding agents.
//
// Adding support for a new agent means implementing Agent in a new sub-package
// and registering it in registry.go — nothing else in Corral needs to
// change. The CLI grows a `corral <name>` shortcut, the guest provisioning
// gains the agent's install script, and a persistent state directory is
// mounted automatically.
package agent

import (
	"fmt"
	"sort"
	"strings"
)

// GuestStateRoot is where agent state directories are mounted inside the box.
// The state for agent X lives at GuestStateRoot/X.
const GuestStateRoot = "/corral/agents"

// LaunchOptions carries the per-run choices that influence the agent argv.
type LaunchOptions struct {
	// Yolo asks the agent to skip its own permission prompts (the box is the
	// safety boundary). When false the agent runs with its normal prompts.
	Yolo bool
	// Args are extra arguments the user passed after the agent name.
	Args []string
	// Interactive is false for `corral <agent> -p ...` style one-shots; the
	// agent may use it to pick a non-interactive mode.
	Interactive bool
}

// Agent is one supported AI coding tool.
type Agent interface {
	// Name is the CLI shortcut and the state-directory name, e.g. "claude".
	Name() string
	// Summary is a one-line description for help output.
	Summary() string
	// ProvisionScript returns a bash script, run once as the box user when the
	// box is created, that installs the agent. It must be idempotent.
	ProvisionScript() string
	// GuestEnv returns environment variables that must be set inside the box
	// for every invocation of the agent (e.g. to relocate its config into the
	// mounted state directory). stateDir is the guest path of the agent's
	// persistent state (GuestStateRoot/<name>).
	GuestEnv(stateDir string) map[string]string
	// ForwardEnv lists host variables the agent needs for authentication that
	// may be forwarded when present on the host (e.g. ANTHROPIC_API_KEY).
	ForwardEnv() []string
	// Binary is the executable name inside the box (e.g. "claude"). A wrapper
	// with the same name is installed in /opt/corral/bin so that typing it
	// in `corral shell` applies YoloArgs when CORRAL_YOLO=1.
	Binary() string
	// YoloArgs are the flags that make the agent skip its own permission
	// prompts. Empty when the agent has no such mode.
	YoloArgs() []string
	// Argv builds the command executed inside the box.
	Argv(opts LaunchOptions) []string
	// VersionArgv is a command that prints the installed version, used by
	// `corral status`/doctor. Nil if unsupported.
	VersionArgv() []string
	// HostConfigImports maps host files/dirs (relative to $HOME) to paths
	// relative to the agent state dir that `corral <agent> --import-config`
	// copies. Credentials must never be listed here.
	HostConfigImports() map[string]string
	// LoginHint tells the user how to authenticate inside the box.
	LoginHint() string
}

// StateSeeder is an optional interface for agents whose *fresh* state
// directory needs a file before the first run — typically an "onboarding
// done" flag, so a token forwarded from the host is used instead of the
// agent stopping at its first-run wizard. The script runs as the box user
// after the state directory exists and must be idempotent: it may only
// create what is missing, never touch a real login.
type StateSeeder interface {
	SeedStateScript(stateDir string) string
}

var registry = map[string]Agent{}

// Register adds an agent. It panics on duplicate names because that is a
// programming error caught at init time.
func Register(a Agent) {
	n := a.Name()
	if _, dup := registry[n]; dup {
		panic(fmt.Sprintf("agent %q registered twice", n))
	}
	registry[n] = a
}

// Lookup returns the agent with the given name.
func Lookup(name string) (Agent, bool) {
	a, ok := registry[strings.ToLower(name)]
	return a, ok
}

// All returns every registered agent sorted by name.
func All() []Agent {
	out := make([]Agent, 0, len(registry))
	for _, a := range registry {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// Names returns the registered agent names sorted.
func Names() []string {
	all := All()
	names := make([]string, len(all))
	for i, a := range all {
		names[i] = a.Name()
	}
	return names
}

// StateDir returns the guest path for an agent's state directory.
func StateDir(name string) string {
	return GuestStateRoot + "/" + name
}

// SeedDir is where the host's agent state is mounted read-only for
// agent_state = "seeded"; it is copied into StateDir on first boot.
func SeedDir(name string) string {
	return GuestStateRoot + "-seed/" + name
}
