package box

import (
	"fmt"

	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/corral-sh/corral/internal/agent"
	_ "github.com/corral-sh/corral/internal/agent/claude"
	"github.com/corral-sh/corral/internal/broker"
	"github.com/corral-sh/corral/internal/config"
	"github.com/corral-sh/corral/internal/guest"
	"github.com/corral-sh/corral/internal/policy"
)

func TestNameFor(t *testing.T) {
	n, err := NameFor("/Users/x/Code/inspect-be-api-service", "", 40)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(n, "inspect-be-api-s-") || len(n) != 16+1+6 {
		t.Errorf("name %q", n)
	}
	n2, _ := NameFor("/Users/y/Code/inspect-be-api-service", "", 40)
	if n == n2 {
		t.Error("different paths must yield different names")
	}
	if o, _ := NameFor("/a", "custom", 40); o != "custom" {
		t.Error("override ignored")
	}
	if _, err := NameFor("/a", "", 8); err == nil {
		t.Error("tiny budget should error")
	}
	n3, _ := NameFor("/tmp/My Project!!", "", 40)
	if !strings.HasPrefix(n3, "my-project-") {
		t.Errorf("slug %q", n3)
	}
}

func testBox(t *testing.T, f config.File) *Box {
	t.Helper()
	t.Setenv("CORRAL_HOME", filepath.Join(t.TempDir(), "eb"))
	cfg, err := config.Resolve(config.Merge(config.Defaults(), f))
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	return &Box{Name: "test-000000", Project: project, Cfg: cfg, Version: "test"}
}

func TestRenderTemplate(t *testing.T) {
	b := testBox(t, config.File{Toolchains: []string{"node", "go"}, Packages: []string{"jq"}})
	tpl, err := b.Render()
	if err != nil {
		t.Fatal(err)
	}
	if tpl.VMType != "vz" || tpl.MountType != "virtiofs" {
		t.Errorf("driver: %s/%s", tpl.VMType, tpl.MountType)
	}
	if tpl.SSH.LoadDotSSHPubKeys {
		t.Error("host ssh keys must never be loaded")
	}
	if len(tpl.Mounts) < 2 || tpl.Mounts[0].Location != b.Project || !tpl.Mounts[0].Writable {
		t.Errorf("project mount: %+v", tpl.Mounts)
	}
	if tpl.Mounts[1].MountPoint != agent.StateDir("claude") {
		t.Errorf("agent state mount: %+v", tpl.Mounts[1])
	}
	var modes []string
	for _, p := range tpl.Provision {
		modes = append(modes, p.Mode)
	}
	joined := strings.Join(modes, ",")
	if !strings.HasPrefix(joined, "system,system,system,system,user,user,system") {
		t.Errorf("provision order: %s", joined)
	}
	if len(tpl.Probes) != 1 || !strings.Contains(tpl.Probes[0].Script, "claude") {
		t.Errorf("probes: %+v", tpl.Probes)
	}
	_, hash1, _ := b.RenderYAML()
	b.Cfg.CPUs++
	_, hash2, _ := b.RenderYAML()
	if hash1 == hash2 {
		t.Error("template hash must change with config")
	}
}

func TestRenderReadonlyAndPrivateState(t *testing.T) {
	ro, shared := true, false
	b := testBox(t, config.File{ReadonlyProject: &ro, SharedAgentState: &shared})
	tpl, err := b.Render()
	if err != nil {
		t.Fatal(err)
	}
	if tpl.Mounts[0].Writable {
		t.Error("readonly project should not be writable")
	}
	if len(tpl.Mounts) != 1 {
		t.Errorf("private state should not mount agent dirs: %+v", tpl.Mounts)
	}
}

// The git metadata shadow is on by default, rendered for the project path,
// and dropped only when the mount is read-only or the user opts out.
func TestRenderGitMetadataShadow(t *testing.T) {
	hasShadow := func(tpl *Template) bool {
		for _, p := range tpl.Provision {
			if strings.Contains(p.Script, "corral-git-shadow") {
				return p.Mode == "system" && strings.Contains(p.Script, "PROJECT=")
			}
		}
		return false
	}
	b := testBox(t, config.File{})
	tpl, err := b.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !hasShadow(tpl) {
		t.Error("default render must shadow .git/config and .git/hooks")
	}
	for _, p := range tpl.Provision {
		if strings.Contains(p.Script, "corral-git-shadow") && !strings.Contains(p.Script, b.Project) {
			t.Errorf("shadow script must carry the project path %s", b.Project)
		}
	}
	off := false
	tpl, _ = testBox(t, config.File{ProtectGitMetadata: &off}).Render()
	if hasShadow(tpl) {
		t.Error("protect_git_metadata = false must drop the shadow")
	}
	ro := true
	tpl, _ = testBox(t, config.File{ReadonlyProject: &ro}).Render()
	if hasShadow(tpl) {
		t.Error("a read-only project needs no shadow")
	}
}

func TestRenderHide(t *testing.T) {
	tpl, err := testBox(t, config.File{Hide: []string{".env", "secrets/"}}).Render()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range tpl.Provision {
		if strings.Contains(p.Script, "corral-hide") {
			found = true
			if p.Mode != "system" || !strings.Contains(p.Script, "HIDE=") || !strings.Contains(p.Script, "secrets/") {
				t.Errorf("hide provision malformed: %.200s", p.Script)
			}
		}
	}
	if !found {
		t.Error("hide entries must render the hide unit")
	}
	if _, err := testBox(t, config.File{Hide: []string{"../secrets"}}).Render(); err == nil {
		t.Error("hide outside the project must be refused")
	}
	tpl, _ = testBox(t, config.File{}).Render()
	for _, p := range tpl.Provision {
		if strings.Contains(p.Script, "corral-hide") {
			t.Error("no hide entries must render no hide unit")
		}
	}
}

func TestRenderOffline(t *testing.T) {
	project := t.TempDir()
	_ = os.MkdirAll(filepath.Join(project, "scripts"), 0o755)
	_ = os.WriteFile(filepath.Join(project, "scripts", "root.sh"), []byte("#!/bin/bash\n# corral: system\necho hi\n"), 0o755)
	mk := func(net string) *Template {
		b := testBox(t, config.File{Network: &net, Provision: []string{"scripts/root.sh"}})
		b.Project = project
		tpl, err := b.Render()
		if err != nil {
			t.Fatal(err)
		}
		return tpl
	}
	find := func(tpl *Template, needle string) *Provision {
		for i := range tpl.Provision {
			if strings.Contains(tpl.Provision[i].Script, needle) {
				return &tpl.Provision[i]
			}
		}
		return nil
	}
	full := mk(config.NetworkFull)
	if find(full, "corral-offline") != nil {
		t.Error("full network must not install the offline unit")
	}
	if p := find(full, "echo hi"); p == nil || p.Mode != "system" {
		t.Error("a '# corral: system' project script runs as root in full mode")
	}
	if find(full, ".corral/provisioned") == nil {
		t.Error("end-of-provisioning marker missing")
	}
	off := mk(config.NetworkOffline)
	o := find(off, "corral-offline")
	if o == nil || o.Mode != "system" {
		t.Fatal("offline must install the offline unit as system")
	}
	if p := find(off, "echo hi"); p == nil || p.Mode != "user" {
		t.Error("offline mode must run project scripts as the user only")
	}
	// The marker must be provisioned before the lockdown is installed.
	var mi, oi int
	for i, p := range off.Provision {
		if strings.Contains(p.Script, ".corral/provisioned") && !strings.Contains(p.Script, "corral-offline") {
			mi = i
		}
		if strings.Contains(p.Script, "corral-offline") {
			oi = i
		}
	}
	if mi > oi {
		t.Error("provisioned marker must come before the offline unit")
	}
	if _, err := config.Resolve(config.Merge(config.Defaults(), config.File{Network: ptr("lan")})); err == nil {
		t.Error("unknown network mode must be refused")
	}
}

func TestRenderRosetta(t *testing.T) {
	on := true
	if tpl, _ := testBox(t, config.File{}).Render(); tpl.Rosetta != nil {
		t.Error("rosetta off by default")
	}
	old := hostArch
	t.Cleanup(func() { hostArch = old })
	hostArch = "arm64"
	tpl, err := testBox(t, config.File{Rosetta: &on}).Render()
	if err != nil || tpl.Rosetta == nil || !tpl.Rosetta.Enabled || !tpl.Rosetta.Binfmt {
		t.Errorf("rosetta = true must render enabled+binfmt: %+v %v", tpl.Rosetta, err)
	}
	hostArch = "amd64"
	if _, err := testBox(t, config.File{Rosetta: &on}).Render(); err == nil {
		t.Error("rosetta on an Intel host must be refused")
	}
}

func TestRefusesSensitiveMounts(t *testing.T) {
	home, _ := os.UserHomeDir()
	for _, bad := range []string{home, filepath.Join(home, ".ssh"), filepath.Join(home, ".aws")} {
		if _, err := os.Stat(bad); err != nil {
			continue
		}
		if err := policy.ExtraMount(config.Mount{Host: bad, Guest: "/x", Writable: false}); err == nil {
			t.Errorf("%s should be refused", bad)
		}
	}
}

func TestBuildLaunch(t *testing.T) {
	b := testBox(t, config.File{
		Env:         []string{"DEBUG=1", "OPTIONAL_MISSING"},
		EnvFromHost: []string{"GH_TOKEN=RO_TOKEN"},
		GitTokens:   map[string]config.GitToken{"git.example.com": {Token: "GL_TOKEN"}, "deploy.example.com": {Token: "GH_TOKEN", User: "gitlab+deploy-token-1"}},
		GitIdentity: ptr(false),
	})
	ag, _ := agent.Lookup("claude")
	host := map[string]string{
		"RO_TOKEN":          "ro",
		"GH_TOKEN":          "rw-must-not-leak",
		"ANTHROPIC_API_KEY": "sk",
		"GL_TOKEN":          "glpat",
		"TERM":              "xterm",
	}
	spec, err := b.BuildLaunch(ag, []string{"-p", "hi"}, true, host)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Env["GH_TOKEN"] != "ro" {
		t.Errorf("alias must own GH_TOKEN, got %q", spec.Env["GH_TOKEN"])
	}
	if spec.Env["ANTHROPIC_API_KEY"] != "sk" || spec.Env["DEBUG"] != "1" || spec.Env["TERM"] != "xterm" {
		t.Errorf("env: %+v", spec.Env)
	}
	if spec.GitEnv["CORRAL_GIT_TOKEN_GIT_EXAMPLE_COM"] != "glpat" {
		t.Errorf("git token env: %+v", spec.GitEnv)
	}
	// The token variable is read from the session env the aliases built,
	// not the raw host env — GH_TOKEN is the narrowed "ro", never the host's.
	// The per-host user travels next to it.
	if spec.GitEnv["CORRAL_GIT_TOKEN_DEPLOY_EXAMPLE_COM"] != "ro" || spec.GitEnv["CORRAL_GIT_USER_DEPLOY_EXAMPLE_COM"] != "gitlab+deploy-token-1" {
		t.Errorf("git token must resolve from the session env with its user: %+v", spec.GitEnv)
	}
	if _, set := spec.GitEnv["CORRAL_GIT_USER_GIT_EXAMPLE_COM"]; set {
		t.Errorf("no user configured → no CORRAL_GIT_USER_ variable (helper defaults to oauth2)")
	}
	if strings.Join(spec.Argv, " ") != "claude --dangerously-skip-permissions -p hi" {
		t.Errorf("argv: %v", spec.Argv)
	}
	if len(spec.Warnings) != 1 || !strings.Contains(spec.Warnings[0], "OPTIONAL_MISSING") {
		t.Errorf("warnings: %v", spec.Warnings)
	}
	for _, f := range spec.Forwarded {
		if strings.Contains(f, "sk") || strings.Contains(f, "glpat") {
			t.Errorf("forwarded list must not contain values: %v", spec.Forwarded)
		}
	}
	env := spec.ProcessEnv([]string{"PATH=/bin", "CORRAL_FWD_STALE=x"})
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "CORRAL_FWD_ANTHROPIC_API_KEY=sk") || strings.Contains(joined, "CORRAL_FWD_STALE") {
		t.Errorf("process env: %v", env)
	}

	// Ask mode: no yolo flag.
	spec, _ = b.BuildLaunch(ag, nil, false, host)
	if strings.Contains(strings.Join(spec.Argv, " "), "dangerously") || spec.Env["CORRAL_YOLO"] != "0" {
		t.Errorf("ask mode argv: %v", spec.Argv)
	}
}

func TestBuildLaunchAliasFailsClosed(t *testing.T) {
	b := testBox(t, config.File{EnvFromHost: []string{"GH_TOKEN=RO_TOKEN"}})
	if _, err := b.BuildLaunch(nil, []string{"true"}, true, map[string]string{"GH_TOKEN": "rw"}); err == nil {
		t.Fatal("unset alias source must refuse to start")
	}
}

func TestNoEnvPassthrough(t *testing.T) {
	b := testBox(t, config.File{NoEnvPassthrough: ptr(true), GitIdentity: ptr(false)})
	ag, _ := agent.Lookup("claude")
	spec, err := b.BuildLaunch(ag, nil, true, map[string]string{"ANTHROPIC_API_KEY": "sk", "TERM": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := spec.Env["ANTHROPIC_API_KEY"]; ok {
		t.Error("passthrough disabled but key forwarded")
	}
}

func ptr[T any](v T) *T { return &v }

func TestProvisionConfinedToProject(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := testBox(t, config.File{Provision: []string{outside}})
	if _, err := b.Render(); err == nil || !strings.Contains(err.Error(), "relative") {
		t.Fatalf("absolute provision path must be refused, got %v", err)
	}

	b = testBox(t, config.File{Provision: []string{"link.sh"}})
	if err := os.Symlink(outside, filepath.Join(b.Project, "link.sh")); err != nil {
		t.Skip(err)
	}
	if _, err := b.Render(); err == nil || !strings.Contains(err.Error(), "outside the project root") {
		t.Fatalf("symlink out of the project must be refused, got %v", err)
	}

	b = testBox(t, config.File{Provision: []string{"../escape.sh"}})
	_ = os.WriteFile(filepath.Join(b.Project, "..", "escape.sh"), []byte("echo"), 0o600)
	if _, err := b.Render(); err == nil {
		t.Fatal("../ traversal must be refused")
	}

	b = testBox(t, config.File{Provision: []string{"scripts/ok.sh"}})
	_ = os.MkdirAll(filepath.Join(b.Project, "scripts"), 0o755)
	_ = os.WriteFile(filepath.Join(b.Project, "scripts", "ok.sh"), []byte("echo ok\n"), 0o600)
	tpl, err := b.Render()
	if err != nil {
		t.Fatalf("in-project script must load: %v", err)
	}
	found := false
	for _, p := range tpl.Provision {
		if strings.Contains(p.Script, "echo ok") {
			found = true
		}
	}
	if !found {
		t.Error("in-project script not embedded")
	}
}

func TestIdleSinceAndSessions(t *testing.T) {
	old := pidAlive
	t.Cleanup(func() { pidAlive = old })
	alive := map[int]bool{100: true}
	pidAlive = func(pid int) bool { return alive[pid] }

	m := &Meta{LastUsed: time.Now().Add(-2 * time.Hour)}
	if since, ok := m.IdleSince(); !ok || time.Since(since) < time.Hour {
		t.Error("a box without sessions is idle since last use")
	}
	m.ActiveSessions = []Session{{PID: 100}, {PID: 999}}
	if _, ok := m.IdleSince(); ok {
		t.Error("a live session means not idle")
	}
	if live := m.pruneSessions(); len(live) != 1 || live[0].PID != 100 {
		t.Errorf("dead pid must be pruned: %+v", live)
	}
	alive[100] = false
	m.LastSessionEnd = time.Now().Add(-45 * time.Minute)
	since, ok := m.IdleSince()
	if !ok || time.Since(since) < 44*time.Minute {
		t.Error("with only dead sessions the box is idle since the last session end")
	}
}

func TestParseIdleStop(t *testing.T) {
	for in, want := range map[string]time.Duration{"30m": 30 * time.Minute, "2h": 2 * time.Hour, "off": 0, "0": 0, "": 0} {
		if got, err := config.ParseIdleStop(in); err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"30", "10s", "soon"} {
		if _, err := config.ParseIdleStop(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestGoldenTemplate(t *testing.T) {
	// rosetta = true is refused on non-arm64 hosts (CI runs on amd64); the
	// test is about the golden subset, so pin the arch.
	old := hostArch
	t.Cleanup(func() { hostArch = old })
	hostArch = "arm64"
	on := true
	b := testBox(t, config.File{Toolchains: []string{"node", "go"}, Packages: []string{"jq"}, Hide: []string{".env"}, Network: ptrS(config.NetworkOffline), Rosetta: &on})
	g, err := b.GoldenTemplate()
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Mounts) != 0 {
		t.Errorf("golden must have no mounts: %+v", g.Mounts)
	}
	if g.Rosetta != nil {
		t.Error("golden must not carry rosetta")
	}
	for _, p := range g.Provision {
		for _, banned := range []string{"installing project packages", "corral-hide", "corral-git-shadow", "corral-offline", "provisioned", "CORRAL_PROFILE_EOF"} {
			if strings.Contains(p.Script, banned) {
				t.Errorf("golden must not contain project-specific provisioning (%s)", banned)
			}
		}
	}
	var hasNode, hasClaude bool
	for _, p := range g.Provision {
		hasNode = hasNode || strings.Contains(p.Script, "node")
		hasClaude = hasClaude || strings.Contains(p.Script, "claude")
	}
	if !hasNode || !hasClaude {
		t.Error("golden must carry toolchains and agents")
	}
	if len(g.Probes) == 0 {
		t.Error("golden keeps the readiness probes so `start` waits for the agents")
	}
	n1, _ := b.GoldenName()
	n2, _ := testBox(t, config.File{Toolchains: []string{"node", "go"}, Packages: []string{"other"}}).GoldenName()
	if !strings.HasPrefix(n1, GoldenPrefix) || n1 != n2 {
		t.Errorf("golden name must depend only on the golden template: %s vs %s", n1, n2)
	}
	n4, _ := testBox(t, config.File{Toolchains: []string{"node", "go"}, Memory: ptrS("16GiB"), CPUs: ptr(8)}).GoldenName()
	if n4 != n1 {
		t.Error("cpus/memory must not create a new golden")
	}
	n3, _ := testBox(t, config.File{Toolchains: []string{"python"}}).GoldenName()
	if n3 == n1 {
		t.Error("different toolchains must give a different golden")
	}
	if len(n1) > 24 {
		t.Errorf("golden name too long for Lima sockets: %s", n1)
	}
	// The full render still carries everything.
	full, err := b.Render()
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Mounts) == 0 || len(full.Provision) <= len(g.Provision) {
		t.Error("project render must be a superset of the golden")
	}
}

func ptrS(s string) *string { return &s }

func TestOverlayInstanceYAML(t *testing.T) {
	p := filepath.Join(t.TempDir(), "lima.yaml")
	_ = os.WriteFile(p, []byte("images:\n- location: https://x/ubuntu.img\nmounts: []\ncpus: 4\n"), 0o600)
	if err := overlayInstanceYAML(p, []byte("base:\n- template:_images/ubuntu-24.04\nmounts:\n- location: /p\ncpus: 2\n")); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	raw, _ := os.ReadFile(p)
	_ = yaml.Unmarshal(raw, &got)
	if _, has := got["base"]; has {
		t.Error("instance yaml must not carry base")
	}
	if got["images"] == nil || got["cpus"] != 2 || len(got["mounts"].([]any)) != 1 {
		t.Errorf("overlay wrong: %+v", got)
	}
}

func TestRenderBroker(t *testing.T) {
	net := config.NetworkBroker
	b := testBox(t, config.File{Network: &net})
	tpl, err := b.Render()
	if err != nil {
		t.Fatal(err)
	}
	var bp *Provision
	for i := range tpl.Provision {
		if strings.Contains(tpl.Provision[i].Script, "corral_broker") {
			bp = &tpl.Provision[i]
		}
	}
	if bp == nil || bp.Mode != "system" {
		t.Fatal("broker mode must install the broker unit as system")
	}
	port := fmt.Sprintf("PORT=%d", broker.PortFor(b.Name))
	if !strings.Contains(bp.Script, port) || !strings.Contains(bp.Script, fmt.Sprintf("tcp dport %s accept", "${PORT}")) {
		t.Errorf("broker script must carry the box's port (%s)", port)
	}
	// Same box name → same template hash: the port must not vary.
	_, h1, _ := b.RenderYAML()
	_, h2, _ := b.RenderYAML()
	if h1 != h2 {
		t.Error("broker template hash is not stable")
	}
	for _, p := range tpl.Provision {
		if strings.Contains(p.Script, "corral-offline") {
			t.Error("broker mode must not also install offline")
		}
	}
}

func TestRenderSeededAgentState(t *testing.T) {
	mode := config.AgentStateSeeded
	b := testBox(t, config.File{AgentState: &mode})
	tpl, err := b.Render()
	if err != nil {
		t.Fatal(err)
	}
	var seed *Mount
	for i := range tpl.Mounts {
		if strings.Contains(tpl.Mounts[i].MountPoint, "-seed/") {
			seed = &tpl.Mounts[i]
		}
		if tpl.Mounts[i].MountPoint == agent.StateDir("claude") {
			t.Error("seeded mode must not mount the host state at the live path")
		}
	}
	if seed == nil || seed.Writable {
		t.Fatalf("seeded mode needs a read-only seed mount, got %+v", tpl.Mounts)
	}
	found := false
	for _, p := range tpl.Provision {
		if strings.Contains(p.Script, "seeded agent state") && p.Mode == "user" {
			found = true
		}
	}
	if !found {
		t.Error("seeded mode must copy the seed on first boot")
	}
	// Alias compatibility: shared_agent_state = false still means isolated.
	f := config.Defaults()
	f.SharedAgentState = ptr(false)
	c, err := config.Resolve(f)
	if err != nil || c.AgentState != config.AgentStateIsolated || c.SharedAgentState {
		t.Fatalf("alias: %+v %v", c, err)
	}
}

// Every provision script — built-in, lockdown unit, repository script — gets
// the generated environment header so CORRAL_USER is always defined (regression test:
// the docker toolchain targeted uid 1000 / "ubuntu", neither of which is the
// box user, and silently left them out of the docker group).
func TestEveryProvisionScriptHasEnvHeader(t *testing.T) {
	project := t.TempDir()
	_ = os.MkdirAll(filepath.Join(project, "scripts"), 0o755)
	_ = os.WriteFile(filepath.Join(project, "scripts", "root.sh"), []byte("#!/bin/bash\n# corral: system\necho hi\n"), 0o755)
	net := config.NetworkBroker
	iso := config.AgentStateIsolated
	b := testBox(t, config.File{Network: &net, Toolchains: []string{"docker"}, Provision: []string{"scripts/root.sh"}, AgentState: &iso})
	b.Project = project
	tpl, err := b.Render()
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range tpl.Provision {
		lines := strings.SplitN(p.Script, "\n", 3)
		if !strings.HasPrefix(lines[0], "#!") || !strings.Contains(lines[1], "Corral provision environment") {
			t.Errorf("provision[%d] (%s) lacks the env header:\n%.200s", i, p.Mode, p.Script)
		}
		if !strings.Contains(p.Script, "export CORRAL_NETWORK=broker\n") || !strings.Contains(p.Script, "export CORRAL_USER CORRAL_HOME\n") {
			t.Errorf("provision[%d] header incomplete", i)
		}
		if strings.Contains(p.Script, "id -un 1000") || strings.Contains(p.Script, "echo ubuntu") || strings.Contains(p.Script, "$SUDO_USER") {
			t.Errorf("provision[%d] still guesses the box user:\n%s", i, p.Script)
		}
		if strings.Contains(p.Script, "usermod -aG docker") && strings.Contains(p.Script, "|| true") {
			t.Errorf("docker group membership must not be best-effort")
		}
	}
	// Goldens are shared across network/source modes: their header carries
	// neither, or every mode would get its own golden.
	g, err := b.GoldenTemplate()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range g.Provision {
		if strings.Contains(p.Script, "CORRAL_NETWORK=") || !strings.Contains(p.Script, "export CORRAL_USER CORRAL_HOME\n") {
			t.Errorf("golden provision header wrong:\n%.300s", p.Script)
		}
	}
}

// Agent state seeding (onboarding flag, seeded login copy) runs after the
// system step that creates the per-box state directory; before that step a
// user-mode script cannot create anything under the root-owned /corral/agents.
func TestStateSeedingRunsAfterStateDirExists(t *testing.T) {
	seeded := config.AgentStateSeeded
	tpl, err := testBox(t, config.File{AgentState: &seeded}).Render()
	if err != nil {
		t.Fatal(err)
	}
	dir, onboarding, seedCopy := -1, -1, -1
	for i, p := range tpl.Provision {
		switch {
		case strings.Contains(p.Script, "install -d -m 0700 -o \"$CORRAL_USER\""):
			dir = i
		case strings.Contains(p.Script, "hasCompletedOnboarding"):
			onboarding = i
		case strings.Contains(p.Script, "seeded agent state from"):
			seedCopy = i
		}
	}
	if dir < 0 || onboarding < 0 || seedCopy < 0 {
		t.Fatalf("missing steps: dir=%d onboarding=%d seedCopy=%d", dir, onboarding, seedCopy)
	}
	if onboarding < dir || seedCopy < dir {
		t.Errorf("seeding (%d, %d) must run after the state dir step (%d)", onboarding, seedCopy, dir)
	}
	for _, p := range tpl.Provision {
		if strings.Contains(p.Script, "hasCompletedOnboarding") && p.Mode != "user" {
			t.Error("onboarding seed must run as the box user")
		}
	}
	g, _ := testBox(t, config.File{}).GoldenTemplate()
	for _, p := range g.Provision {
		if strings.Contains(p.Script, "hasCompletedOnboarding") {
			t.Error("goldens carry no per-box state seeding")
		}
	}
}

func TestBoxDirsTemplate(t *testing.T) {
	b := testBox(t, config.File{BoxDirs: []string{"node_modules", "build/"}})
	tpl, err := b.Render()
	if err != nil {
		t.Fatal(err)
	}
	var found *Provision
	for i := range tpl.Provision {
		if strings.Contains(tpl.Provision[i].Script, "boxdirs.conf") {
			found = &tpl.Provision[i]
		}
	}
	if found == nil || found.Mode != "system" {
		t.Fatal("box_dirs must install the boxdirs unit as a system step")
	}
	if !strings.Contains(found.Script, "DIRS='node_modules\nbuild'") || !strings.Contains(found.Script, "PROJECT="+b.Project) {
		t.Errorf("boxdirs.conf content:\n%.400s", found.Script)
	}
	if !strings.Contains(tpl.Provision[0].Script, "for unit in git-shadow boxdirs hide offline broker") {
		t.Error("corral-launch must re-apply the boxdirs unit before every session")
	}
	// Clone mode: nothing is mounted, everything is on the box disk already.
	src := config.SourceClone
	c := testBox(t, config.File{BoxDirs: []string{"node_modules"}, Source: &src})
	c.Repo = "https://example.com/x.git"
	ct, err := c.Render()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ct.Provision {
		if strings.Contains(p.Script, "boxdirs.conf") {
			t.Error("box_dirs must not apply in clone mode")
		}
	}
	if _, err := testBox(t, config.File{BoxDirs: []string{"../outside"}}).Render(); err == nil {
		t.Error("box_dirs outside the project must be refused")
	}
}

func TestKeychainEnv(t *testing.T) {
	orig := KeychainLookup
	t.Cleanup(func() { KeychainLookup = orig })
	asked := []string{}
	KeychainLookup = func(name string) (string, error) {
		asked = append(asked, name)
		if name == "CLAUDE_CODE_OAUTH_TOKEN" {
			return "kc-secret", nil
		}
		return "", ErrKeychainNotFound
	}
	b := testBox(t, config.File{KeychainEnv: []string{"CLAUDE_CODE_OAUTH_TOKEN"}})
	spec, err := b.BuildLaunch(nil, []string{"true"}, true, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Env["CLAUDE_CODE_OAUTH_TOKEN"] != "kc-secret" || !contains(spec.Forwarded, "CLAUDE_CODE_OAUTH_TOKEN<-keychain") {
		t.Errorf("keychain value not forwarded: %+v", spec)
	}
	// An exported variable wins and the Keychain is not consulted.
	asked = nil
	spec, err = b.BuildLaunch(nil, []string{"true"}, true, map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "env-secret"})
	if err != nil || spec.Env["CLAUDE_CODE_OAUTH_TOKEN"] != "env-secret" || len(asked) != 0 {
		t.Errorf("env must win: %v %+v asked=%v", err, spec.Env, asked)
	}
	// Missing item: refuse to start, name the fix.
	b = testBox(t, config.File{KeychainEnv: []string{"OTHER_TOKEN"}})
	if _, err := b.BuildLaunch(nil, []string{"true"}, true, map[string]string{}); err == nil || !strings.Contains(err.Error(), "security add-generic-password") {
		t.Errorf("missing keychain item must refuse with the fix: %v", err)
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

func TestProjectProvisionScriptsAreRecorded(t *testing.T) {
	project := t.TempDir()
	_ = os.MkdirAll(filepath.Join(project, "scripts"), 0o755)
	_ = os.WriteFile(filepath.Join(project, "scripts", "setup.sh"), []byte("#!/bin/bash\nexit 3\n"), 0o755)
	b := testBox(t, config.File{Provision: []string{"scripts/setup.sh"}})
	b.Project = project
	tpl, err := b.Render()
	if err != nil {
		t.Fatal(err)
	}
	var wrapped string
	for _, p := range tpl.Provision {
		if strings.Contains(p.Script, "repository provision script") {
			wrapped = p.Script
		}
	}
	if wrapped == "" || !strings.Contains(wrapped, "exit 3") || !strings.Contains(wrapped, guest.ProvisionFailureDir) {
		t.Fatalf("project script must be wrapped:\n%s", wrapped)
	}
	if !strings.HasPrefix(wrapped, "#!/bin/bash\n# --- Corral provision environment") {
		t.Error("wrapper keeps the env header first")
	}
}

// env_file: values fill in after the host env and before the Keychain,
// the audit tag says so, and an unsafe file refuses the launch.
func TestBuildLaunchEnvFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, "env")
	if err := os.WriteFile(path, []byte("# queue credentials\nexport GITLAB_NPM_TOKEN='from-file'\nANTHROPIC_API_KEY=\"file-key\"\nRO_TOKEN=file-ro\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := config.File{
		EnvFile:     ptr(path),
		KeychainEnv: []string{"GITLAB_NPM_TOKEN"},
		EnvFromHost: []string{"GH_TOKEN=RO_TOKEN"},
		GitTokens:   map[string]config.GitToken{"git.example.com": {Token: "GITLAB_NPM_TOKEN"}},
		GitIdentity: ptr(false),
	}
	b := testBox(t, f)
	orig := KeychainLookup
	t.Cleanup(func() { KeychainLookup = orig })
	KeychainLookup = func(string) (string, error) {
		t.Fatal("env_file must be consulted before the Keychain")
		return "", nil
	}
	ag, _ := agent.Lookup("claude")
	spec, err := b.BuildLaunch(ag, nil, false, map[string]string{"ANTHROPIC_API_KEY": "exported-wins"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Env["GITLAB_NPM_TOKEN"] != "from-file" || spec.Env["ANTHROPIC_API_KEY"] != "exported-wins" || spec.Env["GH_TOKEN"] != "file-ro" {
		t.Errorf("env: %+v", spec.Env)
	}
	if spec.GitEnv["CORRAL_GIT_TOKEN_GIT_EXAMPLE_COM"] != "from-file" {
		t.Errorf("git token must come through the env_file too: %+v", spec.GitEnv)
	}
	joined := strings.Join(spec.Forwarded, " ")
	if !strings.Contains(joined, "GITLAB_NPM_TOKEN<-env_file") || !strings.Contains(joined, "GH_TOKEN<-RO_TOKEN<-env_file") || strings.Contains(joined, "from-file") {
		t.Errorf("forwarded tags: %v", spec.Forwarded)
	}

	// Unsafe files refuse.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := b.BuildLaunch(ag, nil, false, map[string]string{}); err == nil || !strings.Contains(err.Error(), "readable by others") {
		t.Errorf("0644 env_file must refuse, got %v", err)
	}
	outside := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(outside, []byte("A=b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadEnvFile(outside); err == nil || !strings.Contains(err.Error(), "home directory") {
		t.Errorf("file outside home must refuse, got %v", err)
	}
	if err := os.WriteFile(path, []byte("not a pair\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil { // WriteFile keeps the existing 0644
		t.Fatal(err)
	}
	if _, err := LoadEnvFile(path); err == nil || !strings.Contains(err.Error(), "KEY=value") {
		t.Errorf("malformed line must refuse, got %v", err)
	}
}

// toolchain_versions: the pin reaches the script as an exported
// variable and makes a different golden; the commit form travels too.
func TestToolchainVersionsShapeGolden(t *testing.T) {
	base := testBox(t, config.File{Toolchains: []string{"flutter"}})
	pinned := testBox(t, config.File{Toolchains: []string{"flutter"}, ToolchainVersions: map[string]string{"flutter": "3.44.2@0123456789abcdef0123456789abcdef01234567"}})
	n1, _ := base.GoldenName()
	n2, _ := pinned.GoldenName()
	if n1 == n2 {
		t.Fatal("a pinned toolchain version must produce its own golden")
	}
	tpl, err := pinned.Render()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, p := range tpl.Provision {
		if strings.Contains(p.Script, "export CORRAL_FLUTTER_VERSION=3.44.2\n") && strings.Contains(p.Script, "export CORRAL_FLUTTER_COMMIT=0123456789abcdef0123456789abcdef01234567\n") {
			found = true
		}
	}
	if !found {
		t.Error("flutter script must receive the pinned version and commit")
	}
	if v, c := config.SplitToolchainVersion("3.44.2"); v != "3.44.2" || c != "" {
		t.Errorf("split without commit: %q %q", v, c)
	}
}

// api_brokers: the box gets the route URL, never the token.
func TestBuildLaunchAPIBrokers(t *testing.T) {
	b := testBox(t, config.File{GitIdentity: ptr(false), APIBrokers: []config.APIBroker{{Name: "gitlab", Upstream: "https://git.example.com", Token: "GITLAB_TOKEN", Header: "PRIVATE-TOKEN", Allow: []string{"GET /**"}}}})
	spec, err := b.BuildLaunch(nil, []string{"true"}, false, map[string]string{"GITLAB_TOKEN": "glpat-secret"})
	if err != nil {
		t.Fatal(err)
	}
	want := APIBaseURL(b.Name, "gitlab")
	if spec.Env["CORRAL_API_GITLAB"] != want || !strings.HasPrefix(want, "http://192.168.5.2:") || !strings.HasSuffix(want, "/gitlab") {
		t.Errorf("api env: %q", spec.Env["CORRAL_API_GITLAB"])
	}
	for k, v := range spec.Env {
		if v == "glpat-secret" {
			t.Errorf("the token must never enter the box (%s)", k)
		}
	}
	if !NeedsBroker(b.Cfg) {
		t.Error("api_brokers need the broker child in any network mode")
	}
}

// An item that exists but cannot be read must not be reported as missing —
// the "add one" remedy created duplicate items on the tester's runner.
func TestKeychainUnreadableIsNotMissing(t *testing.T) {
	orig := KeychainLookup
	t.Cleanup(func() { KeychainLookup = orig })
	for _, tc := range []struct {
		err       error
		want, not string
	}{
		{ErrKeychainNotFound, "add one", "-A"},
		{ErrKeychainDenied, "-A", "add one"},
		{ErrKeychainNoInteraction, "env_file", "add one"},
	} {
		KeychainLookup = func(string) (string, error) { return "", tc.err }
		b := testBox(t, config.File{KeychainEnv: []string{"TOK"}})
		_, err := b.BuildLaunch(nil, []string{"true"}, true, map[string]string{})
		if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), tc.not) {
			t.Errorf("%v: want %q and not %q in: %v", tc.err, tc.want, tc.not, err)
		}
		if !strings.Contains(err.Error(), tc.err.Error()) {
			t.Errorf("cause must be named: %v", err)
		}
	}
}

// Strict (no_env_passthrough) drops forward_env on purpose, but a variable
// the host has set must be reported, naming the explicit paths that still work.
func TestSuppressedForwardEnvIsReported(t *testing.T) {
	b := testBox(t, config.File{Profile: ptr(config.ProfileStrict), ForwardEnv: []string{"MY_TOKEN"}, GitIdentity: ptr(false)})
	policy.ApplyProfile(b.Cfg) // what policy.Load does after merging
	ag, _ := agent.Lookup("claude")
	spec, err := b.BuildLaunch(ag, nil, true, map[string]string{"MY_TOKEN": "x", "CLAUDE_CODE_OAUTH_TOKEN": "y", "UNSET_ONE": ""})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := spec.Env["MY_TOKEN"]; ok {
		t.Error("strict must not forward")
	}
	joined := strings.Join(spec.Warnings, "\n")
	for _, want := range []string{"forward_env MY_TOKEN", "strict", "CLAUDE_CODE_OAUTH_TOKEN", "keychain_env"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings must mention %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "UNSET_ONE") {
		t.Error("a variable the host does not have is not a suppression")
	}
	// keychain_env still delivers under strict — the explicit path.
	orig := KeychainLookup
	t.Cleanup(func() { KeychainLookup = orig })
	KeychainLookup = func(string) (string, error) { return "kc", nil }
	b = testBox(t, config.File{Profile: ptr(config.ProfileStrict), KeychainEnv: []string{"MY_TOKEN"}, GitIdentity: ptr(false)})
	policy.ApplyProfile(b.Cfg)
	spec, err = b.BuildLaunch(nil, []string{"true"}, true, map[string]string{})
	if err != nil || spec.Env["MY_TOKEN"] != "kc" || len(spec.Warnings) != 0 {
		t.Errorf("keychain_env must work under strict without a warning: %v %+v", err, spec)
	}
}

// A session registered before the box exists is adopted by Create's
// metadata, so a booting box is never idle to a concurrent sweep.
func TestSessionBeforeMetadataIsAdopted(t *testing.T) {
	old := pidAlive
	t.Cleanup(func() { pidAlive = old })
	pidAlive = func(int) bool { return true }
	b := &Box{}
	if b.SessionOpen() {
		t.Fatal("no session yet")
	}
	b.SessionStart()
	if !b.SessionOpen() {
		t.Fatal("pending session must count as open")
	}
	b.Meta = &Meta{Name: "x", LastUsed: time.Now().Add(-3 * time.Hour), LastSessionEnd: time.Now().Add(-3 * time.Hour)}
	b.adoptPendingSession()
	if _, idle := b.Meta.IdleSince(); idle {
		t.Error("a box booting for a session is not idle, however old its last session is")
	}
	if len(b.Meta.ActiveSessions) != 1 || b.Meta.ActiveSessions[0].PID != os.Getpid() {
		t.Errorf("session not adopted: %+v", b.Meta.ActiveSessions)
	}
	b.SessionStart() // idempotent
	if len(b.Meta.ActiveSessions) != 1 {
		t.Errorf("SessionStart must not duplicate: %+v", b.Meta.ActiveSessions)
	}
}
