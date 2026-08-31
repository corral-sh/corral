package policy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/corral-sh/corral/internal/config"
)

func loosest() *config.Config {
	c, err := config.Resolve(config.Defaults())
	if err != nil {
		panic(err)
	}
	c.Network = config.NetworkFull
	c.AgentState = config.AgentStateShared
	c.SharedAgentState = true
	c.SSHAgent = true
	c.NoEnvPassthrough = false
	c.ProtectGitMetadata = false
	return c
}

// The guarantee of each profile, stated once. If a profile is changed, this
// is the test that has to change with it.
func TestStrictProfileHasNoSudoAndNoEgress(t *testing.T) {
	c := loosest()
	c.Profile = config.ProfileStrict
	ApplyProfile(c)
	if c.Network != config.NetworkBroker { // broker and offline both remove sudo in the guest
		t.Fatalf("strict must route egress through the broker, got %s", c.Network)
	}
	// A stricter network than the floor survives.
	c.Network = config.NetworkOffline
	ApplyProfile(c)
	if c.Network != config.NetworkOffline {
		t.Fatal("offline is stricter than broker and must be kept")
	}
	if c.AgentState != config.AgentStateIsolated || c.SharedAgentState || c.SSHAgent || !c.NoEnvPassthrough || !c.ProtectGitMetadata {
		t.Fatalf("strict floor not applied: %+v", c)
	}
}

func TestOfflineProfileOnlyTouchesNetwork(t *testing.T) {
	c := loosest()
	c.Profile = config.ProfileOffline
	changed := ApplyProfile(c)
	if c.Network != config.NetworkOffline || len(changed) != 1 || changed[0] != "network" {
		t.Fatalf("offline changed %v, network=%s", changed, c.Network)
	}
	if !c.SharedAgentState || !c.SSHAgent {
		t.Fatal("offline must not touch keys outside its bundle")
	}
}

func TestDefaultProfileChangesNothing(t *testing.T) {
	c := loosest()
	if changed := ApplyProfile(c); len(changed) != 0 {
		t.Fatalf("default changed %v", changed)
	}
}

func TestProfileIsAFloorNotACeiling(t *testing.T) {
	c := loosest()
	c.Profile = config.ProfileOffline
	c.ReadonlyProject = true // stricter than any profile asks; must survive
	c.SSHAgent = false
	ApplyProfile(c)
	if !c.ReadonlyProject || c.SSHAgent {
		t.Fatal("keys tighter than the profile were loosened")
	}
}

func TestProfileIsIdempotent(t *testing.T) {
	c := loosest()
	c.Profile = config.ProfileStrict
	ApplyProfile(c)
	if changed := ApplyProfile(c); len(changed) != 0 {
		t.Fatalf("second apply changed %v", changed)
	}
}

// A project may raise the profile, never lower it; the global file's looser
// keys are still raised to the project's profile floor.
func TestProjectMayOnlyRaiseProfile(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "config.toml")
	proj := t.TempDir()
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(global, "profile = \"offline\"\nssh_agent = true\n")
	write(filepath.Join(proj, config.ProjectFileName), "profile = \"default\"\n")
	if _, err := Load(global, "", proj); err == nil {
		t.Fatal("project lowered the profile and was accepted")
	}
	write(filepath.Join(proj, config.ProjectFileName), "profile = \"strict\"\n")
	cfg, err := Load(global, "", proj)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profile != config.ProfileStrict || cfg.SSHAgent || cfg.Network != config.NetworkBroker {
		t.Fatalf("strict floor not applied over global: %+v", cfg)
	}
}

func TestUnknownProfileRejected(t *testing.T) {
	f := config.Defaults()
	f.Profile = bptr("paranoid")
	if _, err := config.Resolve(f); err == nil {
		t.Fatal("unknown profile accepted")
	}
}
