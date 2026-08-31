package policy

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/corral-sh/corral/internal/config"
)

// Every key a repository can write must have an explicit trust class. Adding
// a field to File without classifying it fails here on purpose.
func TestTrustTableCoversFile(t *testing.T) {
	for _, k := range fileKeys() {
		if _, ok := trustTable[k]; !ok {
			t.Errorf("File key %q has no entry in trustTable — decide whether a repository may set it", k)
		}
	}
	for k := range trustTable {
		if !contains(fileKeys(), k) {
			t.Errorf("trustTable key %q is not a File field", k)
		}
	}
}

func writeProject(t *testing.T, body string) (globalPath, projectDir string) {
	t.Helper()
	dir := t.TempDir()
	projectDir = filepath.Join(dir, "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, config.ProjectFileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "config.toml"), projectDir
}

func TestProjectFileCannotGrantHostAccess(t *testing.T) {
	cases := map[string]string{
		"ssh_agent":     `ssh_agent = true`,
		"name":          `name = "inspect-api-3f9a2c"`,
		"forward_env":   `forward_env = ["GITLAB_TOKEN"]`,
		"env_from_host": `env_from_host = ["GH_TOKEN=GITLAB_TOKEN"]`,
		"mounts":        `mounts = ["~/Code/other-repo:/other"]`,
		"git_tokens":    `git_tokens = { "attacker.example" = "GITLAB_TOKEN" }`,
		"bare env":      `env = ["GITLAB_TOKEN"]`,
		"yolo":          `yolo = true`,
		"readonly":      `readonly_project = false`,
		"shared state":  `shared_agent_state = true`,
		"passthrough":   `no_env_passthrough = false`,
		"git metadata":  `protect_git_metadata = false`,
		"network":       `network = "full"`,
		"source":        `source = "mount"`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			// Global tightens so that the project's value is a real loosening.
			gp, pd := writeProject(t, body)
			_ = os.WriteFile(gp, []byte("yolo = false\nshared_agent_state = false\nreadonly_project = true\nno_env_passthrough = true\nprotect_git_metadata = true\nnetwork = \"offline\"\nsource = \"clone\"\n"), 0o600)
			_, err := Load(gp, "", pd)
			var pe *ProjectPolicyError
			if !errors.As(err, &pe) {
				t.Fatalf("expected ProjectPolicyError, got %v", err)
			}
			if !strings.Contains(err.Error(), "config.toml") {
				t.Errorf("error should tell the user where the key belongs: %v", err)
			}
		})
	}
}

func TestProjectFileMayTightenAndShapeGuest(t *testing.T) {
	gp, pd := writeProject(t, `
cpus = 1
memory = "1GiB"
toolchains = ["go"]
packages = ["jq"]
env = ["APP_ENV=dev", "WITH_EQUALS=a=b"]
provision = ["scripts/setup.sh"]
yolo = false
readonly_project = true
shared_agent_state = false
no_env_passthrough = true
git_identity = false
stop_on_exit = true
default_agent = "claude"
network = "offline"
source = "clone"
`)
	c, err := Load(gp, "", pd)
	if err != nil {
		t.Fatalf("a tightening project file must load: %v", err)
	}
	if c.Yolo || !c.ReadonlyProject || c.SharedAgentState || !c.NoEnvPassthrough || c.GitIdentity || !c.StopOnExit || c.Network != "offline" || c.Source != "clone" {
		t.Errorf("tighten values not applied: %+v", c)
	}
	if c.CPUs != 1 || c.Memory != "1GiB" || !contains(c.Toolchains, "go") || !contains(c.Packages, "jq") {
		t.Errorf("guest-shaping values not applied: %+v", c)
	}
}

// Restating the value the user already chose globally is not a violation.
func TestProjectRestatingGlobalIsAllowed(t *testing.T) {
	gp, pd := writeProject(t, `yolo = true`)
	_ = os.WriteFile(gp, []byte("yolo = true\n"), 0o600)
	if _, err := Load(gp, "", pd); err != nil {
		t.Fatalf("restating the global value must load: %v", err)
	}
}

func TestProjectResourceCap(t *testing.T) {
	gp, pd := writeProject(t, "cpus = "+strconv.Itoa(runtime.NumCPU()+1))
	_, err := Load(gp, "", pd)
	var pe *ProjectPolicyError
	if !errors.As(err, &pe) || !strings.Contains(err.Error(), "cpus") {
		t.Fatalf("cpus above host must be refused, got %v", err)
	}
	if hostMemoryBytes() > 0 {
		gp, pd = writeProject(t, `memory = "4096GiB"`)
		if _, err := Load(gp, "", pd); err == nil || !strings.Contains(err.Error(), "memory") {
			t.Fatalf("memory above half the host must be refused, got %v", err)
		}
	}
}

func TestAllViolationsReportedTogether(t *testing.T) {
	gp, pd := writeProject(t, "ssh_agent = true\nmounts = [\"/tmp/x\"]\nenv = [\"SECRET\"]\n")
	_, err := Load(gp, "", pd)
	var pe *ProjectPolicyError
	if !errors.As(err, &pe) {
		t.Fatalf("got %v", err)
	}
	if len(pe.Violations) != 3 {
		t.Errorf("expected 3 violations, got %d: %v", len(pe.Violations), pe.Violations)
	}
}

func TestSymlinkedProjectFileRefused(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret.toml")
	_ = os.WriteFile(secret, []byte("cpus = 2\n"), 0o600)
	pd := filepath.Join(dir, "proj")
	_ = os.MkdirAll(pd, 0o755)
	if err := os.Symlink(secret, filepath.Join(pd, config.ProjectFileName)); err != nil {
		t.Skip(err)
	}
	if _, err := Load(filepath.Join(dir, "config.toml"), "", pd); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlinked project file must be refused, got %v", err)
	}
}

// The global file is the user's own: everything is allowed there.
func TestGlobalFileIsTrusted(t *testing.T) {
	dir := t.TempDir()
	gp := filepath.Join(dir, "config.toml")
	_ = os.WriteFile(gp, []byte(`
ssh_agent = true
name = "custom"
forward_env = ["X"]
env = ["Y"]
mounts = ["/tmp/x:/x:ro"]
git_tokens = { "git.example.com" = "TOK" }
`), 0o600)
	c, err := Load(gp, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !c.SSHAgent || c.Name != "custom" || len(c.Mounts) != 1 {
		t.Errorf("%+v", c)
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]uint64{"8GiB": 8 << 30, "512MiB": 512 << 20, "2G": 2 << 30, "1.5GiB": 3 << 29} {
		got, err := parseSize(in)
		if err != nil || got != want {
			t.Errorf("%s: got %d,%v want %d", in, got, err, want)
		}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestNetworkTightenOrderAndEgressTrustedOnly(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "config.toml")
	proj := t.TempDir()
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(global, "network = \"broker\"\n")
	write(filepath.Join(proj, config.ProjectFileName), "network = \"full\"\n")
	if _, err := Load(global, "", proj); err == nil {
		t.Fatal("project loosened broker → full and was accepted")
	}
	write(filepath.Join(proj, config.ProjectFileName), "network = \"offline\"\n")
	if _, err := Load(global, "", proj); err != nil {
		t.Fatalf("project tightening broker → offline refused: %v", err)
	}
	write(filepath.Join(proj, config.ProjectFileName), "egress = [\"evil.example\"]\n")
	_, err := Load(global, "", proj)
	if err == nil || !strings.Contains(err.Error(), "egress") {
		t.Fatalf("project egress must be refused, got %v", err)
	}
	// The trusted host-per-project layer may add hosts; defaults stay.
	host := filepath.Join(dir, "host.toml")
	write(host, "egress = [\"registry.npmjs.org:443\"]\n")
	write(filepath.Join(proj, config.ProjectFileName), "")
	cfg, err := Load(global, host, proj)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(cfg.Egress, "registry.npmjs.org:443") || !contains(cfg.Egress, "api.anthropic.com") {
		t.Fatalf("egress = %v", cfg.Egress)
	}
}

func TestAgentStateTightenOrder(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "config.toml")
	proj := t.TempDir()
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(global, "agent_state = \"seeded\"\n")
	write(filepath.Join(proj, config.ProjectFileName), "agent_state = \"shared\"\n")
	if _, err := Load(global, "", proj); err == nil {
		t.Fatal("project loosened seeded → shared and was accepted")
	}
	write(filepath.Join(proj, config.ProjectFileName), "agent_state = \"isolated\"\n")
	cfg, err := Load(global, "", proj)
	if err != nil || cfg.AgentState != config.AgentStateIsolated || cfg.SharedAgentState {
		t.Fatalf("tightening refused or not applied: %v %+v", err, cfg)
	}
	// Deprecated alias in the global file, new key in the project: same axis.
	write(global, "shared_agent_state = false\n")
	write(filepath.Join(proj, config.ProjectFileName), "agent_state = \"seeded\"\n")
	if _, err := Load(global, "", proj); err == nil {
		t.Fatal("isolated (via alias) → seeded is a loosening and must be refused")
	}
}
