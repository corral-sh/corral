package policy

import "github.com/corral-sh/corral/internal/config"

// Profile is a named bundle of guarantees. It is data, not code paths: each
// field that is set is a floor the resolved configuration must meet, and
// TestProfiles asserts the guarantee of each profile once, so it never has to
// be reconstructed from N independent keys.
//
// Semantics are "tighten-only": a key set stricter than the profile stays;
// a key set looser is raised to the profile's value — whichever layer or flag
// set it. That is what makes `profile = "strict"` a statement a reviewer can
// trust without reading the rest of the file.
type Profile struct {
	Name               string
	Network            string // "" = no floor
	AgentState         string // "" = no floor; floor by rank shared → seeded → isolated
	SSHAgent           *bool  // floor false
	NoEnvPassthrough   *bool  // floor true
	ProtectGitMetadata *bool  // floor true
}

var profiles = map[string]Profile{
	config.ProfileDefault: {Name: config.ProfileDefault},
	// offline: nothing leaves the box except to the Mac. sudo is removed in
	// the guest as part of offline mode, so the agent cannot lift the rule.
	config.ProfileOffline: {Name: config.ProfileOffline, Network: config.NetworkOffline},
	// strict: egress only through the allow-list broker on the Mac (sudo
	// removed), plus nothing of the host's beyond the project reaches the box
	// — no shared agent login, no SSH agent, no ambient environment — and the
	// repository's own .git metadata cannot reach the host. A user may still
	// set network = "offline" on top: the profile is a floor.
	config.ProfileStrict: {
		Name:               config.ProfileStrict,
		Network:            config.NetworkBroker,
		AgentState:         config.AgentStateIsolated,
		SSHAgent:           bptr(false),
		NoEnvPassthrough:   bptr(true),
		ProtectGitMetadata: bptr(true),
	},
}

func bptr[T any](v T) *T { return &v }

// ProfileOf returns the bundle for a profile name (default if unknown).
func ProfileOf(name string) Profile {
	if p, ok := profiles[name]; ok {
		return p
	}
	return profiles[config.ProfileDefault]
}

// ApplyProfile raises cfg to the floor of its profile. Called by Load after
// merging, and again by the CLI after flags so a flag cannot loosen below it.
// Returns the keys it changed, for the create summary.
func ApplyProfile(cfg *config.Config) []string {
	p := ProfileOf(cfg.Profile)
	var changed []string
	if p.Network != "" && config.NetworkRank(cfg.Network) < config.NetworkRank(p.Network) {
		cfg.Network = p.Network
		changed = append(changed, "network")
	}
	tighten := func(key string, cur *bool, floor *bool) {
		if floor != nil && *cur != *floor {
			*cur = *floor
			changed = append(changed, key)
		}
	}
	if p.AgentState != "" && config.AgentStateRank(cfg.AgentState) < config.AgentStateRank(p.AgentState) {
		cfg.AgentState = p.AgentState
		cfg.SharedAgentState = cfg.AgentState == config.AgentStateShared
		changed = append(changed, "agent_state")
	}
	tighten("ssh_agent", &cfg.SSHAgent, p.SSHAgent)
	tighten("no_env_passthrough", &cfg.NoEnvPassthrough, p.NoEnvPassthrough)
	tighten("protect_git_metadata", &cfg.ProtectGitMetadata, p.ProtectGitMetadata)
	return changed
}

// ProfileGuarantee is the one-line statement `info` and the create summary print.
func ProfileGuarantee(name string) string {
	switch name {
	case config.ProfileOffline:
		return "no egress (only the Mac), sudo removed"
	case config.ProfileStrict:
		return "egress only via the allow-list broker on the Mac, sudo removed, private agent login, no SSH agent, no ambient env, .git metadata shadowed"
	default:
		return "outbound internet; guarantees come from individual keys"
	}
}
