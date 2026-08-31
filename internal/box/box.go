// Package box is the heart of Corral: it maps a project directory to a Lima
// VM ("box"), renders the VM template from the resolved configuration, keeps
// per-box metadata and builds the launch command for an agent.
package box

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/corral-sh/corral/internal/agent"
	"github.com/corral-sh/corral/internal/broker"
	"github.com/corral-sh/corral/internal/config"
	"github.com/corral-sh/corral/internal/guest"
	"github.com/corral-sh/corral/internal/lima"
	"github.com/corral-sh/corral/internal/paths"
	"github.com/corral-sh/corral/internal/policy"
)

// Meta is what Corral remembers about a box, next to Lima's own state.
type Meta struct {
	Name            string    `json:"name"`
	Project         string    `json:"project"`
	CreatedAt       time.Time `json:"created_at"`
	LastUsed        time.Time `json:"last_used"`
	Sessions        int       `json:"sessions"`
	Agents          []string  `json:"agents"`
	Toolchains      []string  `json:"toolchains"`
	Packages        []string  `json:"packages"`
	ReadonlyProject bool      `json:"readonly_project"`
	SharedState     bool      `json:"shared_agent_state"`
	AgentState      string    `json:"agent_state,omitempty"`
	TemplateHash    string    `json:"template_hash"`
	CPUs            int       `json:"cpus"`
	Memory          string    `json:"memory"`
	Disk            string    `json:"disk"`
	CorralVersion   string    `json:"corral_version"`
	LimaVersion     string    `json:"lima_version"`
	// Golden marks a golden image (see golden.go); GoldenFrom names the golden a
	// box was cloned from ("" = built from scratch).
	Golden     bool   `json:"golden,omitempty"`
	GoldenFrom string `json:"golden_from,omitempty"`
	// Repo is the --repo URL[@ref] a clone-mode box without a checkout was
	// created for, so later commands resolve the same box and mode.
	Repo string `json:"repo,omitempty"`
	// Session tracking for idle_stop (see idle.go).
	ActiveSessions []Session `json:"active_sessions,omitempty"`
	// BrokerPID is the egress broker process on the Mac for network = "broker" (0 = none).
	BrokerPID      int       `json:"broker_pid,omitempty"`
	LastSessionEnd time.Time `json:"last_session_end,omitempty"`
}

// Box couples a project with its configuration and Lima client.
type Box struct {
	Name    string
	Project string
	Cfg     *config.Config
	Lima    *lima.Client
	Meta    *Meta // nil until the box has been created
	Version string
	// pendingSession: SessionStart was called before Meta existed (see idle.go).
	pendingSession bool
	// Repo is the --repo override for clone mode: URL[@ref]. "" = use the
	// local checkout's origin and branch.
	Repo string
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// NameFor derives a stable box name from the project path: a short slug of
// the directory name plus a hash of the absolute path, e.g. "inspect-api-3f9a2c".
func NameFor(project, override string, maxLen int) (string, error) {
	sum := sha256.Sum256([]byte(project))
	h := hex.EncodeToString(sum[:])[:6]
	if override != "" {
		return override, nil
	}
	slug := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(filepath.Base(project)), "-"), "-")
	if slug == "" {
		slug = "box"
	}
	budget := maxLen - len(h) - 1
	if budget < 3 {
		return "", fmt.Errorf("box name budget too small (%d); shorten CORRAL_HOME", maxLen)
	}
	if budget > 16 {
		budget = 16
	}
	if len(slug) > budget {
		slug = strings.Trim(slug[:budget], "-")
	}
	return slug + "-" + h, nil
}

// DefaultNameFor is the box name a project gets without a --box override:
// stable for a project path, and the key of its host-side config file.
func DefaultNameFor(project string) (string, error) {
	abs, err := filepath.Abs(project)
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	maxLen, err := paths.MaxBoxNameLen()
	if err != nil {
		return "", err
	}
	return NameFor(abs, "", maxLen)
}

// Open resolves the box for a project directory.
func Open(project string, cfg *config.Config, version string) (*Box, error) {
	abs, err := filepath.Abs(project)
	if err != nil {
		return nil, err
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("project %s: %w", project, err)
	}
	if err := policy.ProjectPath(abs); err != nil {
		return nil, err
	}
	maxLen, err := paths.MaxBoxNameLen()
	if err != nil {
		return nil, err
	}
	name, err := NameFor(abs, cfg.Name, maxLen)
	if err != nil {
		return nil, err
	}
	if len(name) > maxLen {
		return nil, fmt.Errorf("box name %q is longer than %d characters; shorten `name` or CORRAL_HOME", name, maxLen)
	}
	lh, err := paths.LimaHome()
	if err != nil {
		return nil, err
	}
	lc, err := lima.New(lh)
	if err != nil {
		return nil, err
	}
	b := &Box{Name: name, Project: abs, Cfg: cfg, Lima: lc, Version: version}
	b.Meta, _ = LoadMeta(name)
	return b, nil
}

// OpenDetached builds a Box for lifecycle operations when the project path no
// longer exists on disk (moved or deleted). Only Status/Delete/Stop are
// meaningful on such a box.
func OpenDetached(name, project string, cfg *config.Config, limaHome, version string) (*Box, error) {
	lc, err := lima.New(limaHome)
	if err != nil {
		return nil, err
	}
	b := &Box{Name: name, Project: project, Cfg: cfg, Lima: lc, Version: version}
	b.Meta, _ = LoadMeta(name)
	return b, nil
}

// LoadMeta reads a box's metadata file.
func LoadMeta(name string) (*Meta, error) {
	dir, err := paths.BoxesDir()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, name+".json"))
	if err != nil {
		return nil, err
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("corrupt metadata for %s: %w", name, err)
	}
	return &m, nil
}

// SaveMeta persists metadata.
func SaveMeta(m *Meta) error {
	dir, err := paths.BoxesDir()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, m.Name+".json"), data, 0o600)
}

// DeleteMeta removes the metadata file (ignoring absence).
func DeleteMeta(name string) error {
	dir, err := paths.BoxesDir()
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(dir, name+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// AllMeta lists metadata for every known box.
func AllMeta() ([]*Meta, error) {
	dir, err := paths.BoxesDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []*Meta
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		m, err := LoadMeta(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastUsed.After(out[j].LastUsed) })
	return out, nil
}

// ---------------------------------------------------------------------------
// Template rendering
// ---------------------------------------------------------------------------

// Template is the Lima YAML we generate. Field order matters only for humans
// reading ~/.corral/lima/<box>/lima.yaml.
type Template struct {
	MinimumLimaVersion string      `yaml:"minimumLimaVersion"`
	Base               []string    `yaml:"base"`
	VMType             string      `yaml:"vmType"`
	MountType          string      `yaml:"mountType"`
	CPUs               int         `yaml:"cpus"`
	Memory             string      `yaml:"memory"`
	Disk               string      `yaml:"disk"`
	Mounts             []Mount     `yaml:"mounts"`
	SSH                SSH         `yaml:"ssh"`
	Containerd         Containerd  `yaml:"containerd"`
	HostResolver       HostRes     `yaml:"hostResolver"`
	Rosetta            *Rosetta    `yaml:"rosetta,omitempty"`
	Provision          []Provision `yaml:"provision"`
	Probes             []Probe     `yaml:"probes"`
}

type Mount struct {
	Location   string `yaml:"location"`
	MountPoint string `yaml:"mountPoint,omitempty"`
	Writable   bool   `yaml:"writable"`
}
type SSH struct {
	LoadDotSSHPubKeys bool `yaml:"loadDotSSHPubKeys"`
	ForwardAgent      bool `yaml:"forwardAgent"`
}
type Containerd struct {
	System bool `yaml:"system"`
	User   bool `yaml:"user"`
}
type HostRes struct {
	Enabled bool `yaml:"enabled"`
}

// Rosetta is Lima's vz-only Rosetta 2 integration: amd64 ELF binaries (and
// `docker run --platform linux/amd64`) run near-native inside the arm64 guest.
type Rosetta struct {
	Enabled bool `yaml:"enabled"`
	Binfmt  bool `yaml:"binfmt"`
}

// hostArch is runtime.GOARCH, a variable so tests can exercise the guard.
var hostArch = runtime.GOARCH

type Provision struct {
	Mode   string `yaml:"mode"`
	Script string `yaml:"script"`
}
type Probe struct {
	Mode        string `yaml:"mode"`
	Description string `yaml:"description"`
	Script      string `yaml:"script"`
	Hint        string `yaml:"hint,omitempty"`
}

// GuestImage is the Lima image template every box is built from. Lima pins
// the Ubuntu cloud image by sha256 digest per release, so the base OS is
// reproducible for a given Lima version.
const GuestImage = "template:_images/ubuntu-24.04"

// Agents returns the agent implementations this box is provisioned with.
// Today every box gets every registered agent; the registry is the single
// source of truth for "supported tools".
func (b *Box) Agents() []agent.Agent { return agent.All() }

// Render produces the Lima template for this box.
func (b *Box) Render() (*Template, error) { return b.render(false) }

// render builds the template; golden=true renders the project-independent
// subset a golden image is provisioned with (see golden.go): no mounts, no
// project config, only base + toolchains + agents.
func (b *Box) render(golden bool) (*Template, error) {
	cfg := b.Cfg
	t := &Template{
		MinimumLimaVersion: lima.MinVersion,
		Base:               []string{GuestImage},
		VMType:             "vz",
		MountType:          "virtiofs",
		CPUs:               cfg.CPUs,
		Memory:             cfg.Memory, // for a golden: defaults, see below
		Disk:               cfg.Disk,
		SSH:                SSH{LoadDotSSHPubKeys: false, ForwardAgent: cfg.SSHAgent},
		Containerd:         Containerd{System: false, User: false},
		HostResolver:       HostRes{Enabled: true},
	}
	if golden {
		// Goldens are keyed by their template: pin resources to the defaults so
		// only toolchains and disk (which cannot shrink) create a new golden;
		// the clone's own yaml sets the project's cpus/memory.
		d, _ := config.Resolve(config.Defaults())
		t.CPUs, t.Memory = d.CPUs, d.Memory
	}
	if cfg.Rosetta && !golden {
		if hostArch != "arm64" {
			return nil, fmt.Errorf("rosetta = true needs an Apple Silicon Mac (this host is %s); on Intel, amd64 binaries already run natively — remove the key", hostArch)
		}
		t.Rosetta = &Rosetta{Enabled: true, Binfmt: true}
	}

	clone := cfg.Source == config.SourceClone
	// 1. The project, at its real path so agents see the same paths as you.
	// In clone mode nothing is mounted: the launcher clones into that path.
	if !golden && !clone {
		t.Mounts = append(t.Mounts, Mount{Location: b.Project, MountPoint: b.Project, Writable: !cfg.ReadonlyProject})
	}

	// 2. Agent state. shared: the host directory, read-write. seeded: the
	// host directory read-only at a seed path, copied into the box's own
	// state dir on first boot (see guest.SeedStateScript). isolated: nothing.
	if !golden && cfg.AgentState != config.AgentStateIsolated {
		for _, a := range b.Agents() {
			host, err := paths.AgentStateDir(a.Name())
			if err != nil {
				return nil, err
			}
			if cfg.AgentState == config.AgentStateSeeded {
				t.Mounts = append(t.Mounts, Mount{Location: host, MountPoint: agent.SeedDir(a.Name()), Writable: false})
			} else {
				t.Mounts = append(t.Mounts, Mount{Location: host, MountPoint: agent.StateDir(a.Name()), Writable: true})
			}
		}
	}

	// 3. Extra mounts from config.
	for _, m := range cfg.Mounts {
		if golden {
			break
		}
		if err := policy.ExtraMount(m); err != nil {
			return nil, err
		}
		t.Mounts = append(t.Mounts, Mount{Location: m.Host, MountPoint: m.Guest, Writable: m.Writable})
	}

	// Provisioning: system → toolchains → packages → user → agents → wrappers.
	t.Provision = append(t.Provision, Provision{Mode: "system", Script: guest.Script("base")})
	for _, tc := range cfg.Toolchains {
		s, ok := guest.ToolchainScript(tc)
		if !ok {
			return nil, fmt.Errorf("unknown toolchain %q", tc)
		}
		// A pinned version is exported to the script, and so becomes part of
		// the template — and of the golden's identity.
		if v, ok := cfg.ToolchainVersions[tc]; ok {
			version, commit := config.SplitToolchainVersion(v)
			prefix := "CORRAL_" + strings.ToUpper(tc)
			s = guest.WithProvisionEnv(s, map[string]string{prefix + "_VERSION": version, prefix + "_COMMIT": commit})
		}
		t.Provision = append(t.Provision, Provision{Mode: "system", Script: s})
	}
	if s := guest.PackagesScript(cfg.Packages); s != "" && !golden {
		t.Provision = append(t.Provision, Provision{Mode: "system", Script: s})
	}
	t.Provision = append(t.Provision, Provision{Mode: "user", Script: guest.Script("user-base")})

	env := map[string]string{"CORRAL_NETWORK": cfg.Network, "CORRAL_SOURCE": cfg.Source}
	var wrappers strings.Builder
	wrappers.WriteString("#!/bin/bash\nset -euo pipefail\n")
	for _, a := range b.Agents() {
		// The agent's own installer, followed by a marker the readiness probe
		// waits for (probes run as root, so they cannot rely on the user's PATH).
		script := strings.TrimRight(a.ProvisionScript(), "\n") +
			fmt.Sprintf("\nmkdir -p \"$HOME/.corral\" && touch \"$HOME/.corral/%s.ready\"\n", a.Name())
		t.Provision = append(t.Provision, Provision{Mode: "user", Script: script})
		for k, v := range a.GuestEnv(agent.StateDir(a.Name())) {
			env[k] = v
		}
		if cfg.AgentState != config.AgentStateShared {
			// Per-box state lives on the VM disk instead of a host mount.
			fmt.Fprintf(&wrappers, "install -d -m 0700 -o \"$CORRAL_USER\" %s\n", guest.ShellQuote(agent.StateDir(a.Name())))
		}
		if len(a.YoloArgs()) > 0 {
			fmt.Fprintf(&wrappers, "cat > /opt/corral/bin/%s <<'CORRAL_WRAPPER_EOF'\n%sCORRAL_WRAPPER_EOF\nchmod 0755 /opt/corral/bin/%s\n",
				a.Binary(), guest.WrapperScript(a.Binary(), a.YoloArgs()), a.Binary())
		}
	}
	wrappers.WriteString("cat > /etc/profile.d/corral.sh <<'CORRAL_PROFILE_EOF'\n" + guest.ProfileScript(env) + "CORRAL_PROFILE_EOF\n")
	wrappers.WriteString("chmod 0644 /etc/profile.d/corral.sh\n")
	if golden {
		// Everything below is project-specific and re-runs on the clone's first boot.
		b.appendProbes(t)
		// No network/source here: goldens are keyed by this template's hash
		// and must stay shared across projects with different modes.
		withProvisionEnv(t, nil)
		return t, nil
	}
	t.Provision = append(t.Provision, Provision{Mode: "system", Script: wrappers.String()})
	// State seeding runs after the wrappers step created the per-box state
	// directory (isolated/seeded); before it, a user-mode mkdir under the
	// root-owned /corral/agents failed and the seed was skipped on first boot.
	for _, a := range b.Agents() {
		if cfg.AgentState == config.AgentStateSeeded {
			t.Provision = append(t.Provision, Provision{Mode: "user", Script: guest.SeedStateScript(agent.SeedDir(a.Name()), agent.StateDir(a.Name()))})
		}
		if sd, ok := a.(agent.StateSeeder); ok {
			t.Provision = append(t.Provision, Provision{Mode: "user", Script: sd.SeedStateScript(agent.StateDir(a.Name()))})
		}
	}

	// Protect what the host executes out of the checkout (.git/hooks,
	// .git/config) from in-box edits. Pointless on a read-only mount.
	if clone {
		// The clone target must exist and belong to the box user before the
		// first session (parents like /Users/<you> do not exist in the guest).
		t.Provision = append(t.Provision, Provision{Mode: "system", Script: guest.CloneDirScript(b.GuestPath())})
	}
	if cfg.ProtectGitMetadata && !cfg.ReadonlyProject && !clone {
		t.Provision = append(t.Provision, Provision{Mode: "system", Script: guest.GitShadowScript(b.Project)})
	}
	// hide: shadow listed project paths inside the box. The list comes from the
	// repository too, so every entry is confined to the project first.
	if len(cfg.Hide) > 0 && !clone {
		var hide []string
		for _, h := range cfg.Hide {
			rel, err := policy.HidePath(h)
			if err != nil {
				return nil, err
			}
			hide = append(hide, rel)
		}
		t.Provision = append(t.Provision, Provision{Mode: "system", Script: guest.HideScript(b.Project, hide)})
	}

	// box_dirs: listed project directories live on the box disk (bind mount
	// over the virtiofs mount). Meaningless in clone mode — everything is on
	// the box disk already. Confined to the project like hide.
	if len(cfg.BoxDirs) > 0 && !clone {
		var dirs []string
		for _, d := range cfg.BoxDirs {
			rel, err := policy.BoxDirPath(d)
			if err != nil {
				return nil, err
			}
			dirs = append(dirs, rel)
		}
		t.Provision = append(t.Provision, Provision{Mode: "system", Script: guest.BoxDirsScript(b.Project, dirs)})
	}

	// Custom provision scripts from project config. The list comes from the
	// repository, so every entry must be a regular file inside the project —
	// after resolving symlinks, or a link out of the repo would read any host file.
	for _, p := range cfg.Provision {
		path, err := policy.ProvisionPath(b.Project, p)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path) //nolint:gosec // path was just confined to the project root
		if err != nil {
			return nil, fmt.Errorf("provision script %s: %w", p, err)
		}
		mode := "user"
		// Offline mode exists so the agent cannot undo in-guest controls; a
		// repository script running as root during provisioning could pre-empt
		// them, so project scripts are user-only there.
		if strings.Contains(string(data), "# corral: system") && cfg.Network == config.NetworkFull {
			mode = "system"
		}
		t.Provision = append(t.Provision, Provision{Mode: mode, Script: guest.RecordedProvisionScript(p, string(data))})
	}

	// End-of-provisioning marker, then the offline lockdown that waits for it.
	t.Provision = append(t.Provision, Provision{Mode: "user", Script: guest.ProvisionedMarkerScript})
	switch cfg.Network {
	case config.NetworkOffline:
		t.Provision = append(t.Provision, Provision{Mode: "system", Script: guest.OfflineScript()})
	case config.NetworkBroker:
		// The port is derived from the box name, so the template (and its
		// hash) is stable across restarts of the broker process on the Mac.
		t.Provision = append(t.Provision, Provision{Mode: "system", Script: guest.BrokerScript(broker.PortFor(b.Name))})
	}

	b.appendProbes(t)
	withProvisionEnv(t, cfg)
	return t, nil
}

// withProvisionEnv gives every provision script the generated environment
// header (CORRAL_USER, CORRAL_HOME, CORRAL_NETWORK, CORRAL_SOURCE).
// Applied last so built-in scripts, lockdown units and repository scripts
// all see the same variables.
func withProvisionEnv(t *Template, cfg *config.Config) {
	env := map[string]string{}
	if cfg != nil {
		env["CORRAL_NETWORK"], env["CORRAL_SOURCE"] = cfg.Network, cfg.Source
	}
	for i := range t.Provision {
		t.Provision[i].Script = guest.WithProvisionEnv(t.Provision[i].Script, env)
	}
}

func (b *Box) appendProbes(t *Template) {
	for _, a := range b.Agents() {
		if v := a.VersionArgv(); len(v) > 0 {
			t.Probes = append(t.Probes, Probe{
				Mode:        "readiness",
				Description: a.Name() + " installed",
				Script: fmt.Sprintf("#!/bin/bash\nset -eu\nif ! timeout 900s bash -c 'until ls /home/*/.corral/%s.ready >/dev/null 2>&1; do sleep 3; done'; then\n  echo >&2 \"%s did not become ready\"\n  exit 1\nfi\n",
					a.Name(), a.Name()),
				Hint: a.Name() + " did not finish installing; check `corral logs`",
			})
		}
	}
}

// RenderYAML returns the template as YAML plus its content hash.
func (b *Box) RenderYAML() ([]byte, string, error) {
	t, err := b.Render()
	if err != nil {
		return nil, "", err
	}
	data, err := yaml.Marshal(t)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:])[:12], nil
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// ProvisionFailures returns the recorded failures of repository provision
// scripts from the current boot (see guest.RecordedProvisionScript). Empty
// when every script exited 0. Errors talking to the box are returned as-is.
func (b *Box) ProvisionFailures(ctx context.Context) ([]string, error) {
	out, err := b.Lima.Run(ctx, b.Name, "bash", "-c", "cat "+guest.ProvisionFailureDir+"/*.failed 2>/dev/null || true")
	if err != nil {
		return nil, err
	}
	var fails []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			fails = append(fails, l)
		}
	}
	return fails, nil
}

// State describes what Ensure found / did.
type State int

const (
	StateMissing State = iota
	StateStopped
	StateRunning
)

// Status returns the Lima instance if it exists.
func (b *Box) Status(ctx context.Context) (lima.Instance, State, error) {
	inst, ok, err := b.Lima.Get(ctx, b.Name)
	if err != nil {
		return inst, StateMissing, err
	}
	if !ok {
		return inst, StateMissing, nil
	}
	if inst.Running() {
		return inst, StateRunning, nil
	}
	return inst, StateStopped, nil
}

// Create writes the template and creates+boots the VM.
func (b *Box) Create(ctx context.Context, progress lima.Progress) error {
	data, hash, err := b.RenderYAML()
	if err != nil {
		return err
	}
	dir, err := paths.BoxesDir()
	if err != nil {
		return err
	}
	tpl := filepath.Join(dir, b.Name+".lima.yaml")
	if err := os.WriteFile(tpl, data, 0o600); err != nil {
		return err
	}
	goldenFrom := ""
	if b.Cfg.Golden {
		goldenFrom, err = b.createFromGolden(ctx, progress, data, tpl)
		if err != nil {
			return err
		}
	} else if err := b.Lima.Create(ctx, b.Name, tpl, progress); err != nil {
		return err
	}
	limaVer, _ := b.Lima.Version(ctx)
	now := time.Now()
	b.Meta = &Meta{
		Name:            b.Name,
		Project:         b.Project,
		CreatedAt:       now,
		LastUsed:        now,
		Agents:          agent.Names(),
		Toolchains:      b.Cfg.Toolchains,
		Packages:        b.Cfg.Packages,
		ReadonlyProject: b.Cfg.ReadonlyProject,
		SharedState:     b.Cfg.SharedAgentState,
		AgentState:      b.Cfg.AgentState,
		TemplateHash:    hash,
		CPUs:            b.Cfg.CPUs,
		Memory:          b.Cfg.Memory,
		Disk:            b.Cfg.Disk,
		CorralVersion:   b.Version,
		LimaVersion:     limaVer,
		GoldenFrom:      goldenFrom,
		Repo:            b.Repo,
	}
	b.adoptPendingSession()
	return SaveMeta(b.Meta)
}

// RecoverMeta writes metadata for a box whose VM exists but whose metadata is
// missing (e.g. an interrupted first run). The template hash is recomputed
// from the current config, so drift detection starts from "in sync".
func (b *Box) RecoverMeta(ctx context.Context) error {
	_, hash, err := b.RenderYAML()
	if err != nil {
		return err
	}
	limaVer, _ := b.Lima.Version(ctx)
	now := time.Now()
	b.Meta = &Meta{
		Name: b.Name, Project: b.Project, CreatedAt: now, LastUsed: now,
		Agents: agent.Names(), Toolchains: b.Cfg.Toolchains, Packages: b.Cfg.Packages,
		ReadonlyProject: b.Cfg.ReadonlyProject, SharedState: b.Cfg.SharedAgentState, AgentState: b.Cfg.AgentState,
		TemplateHash: hash, CPUs: b.Cfg.CPUs, Memory: b.Cfg.Memory, Disk: b.Cfg.Disk,
		CorralVersion: b.Version, LimaVersion: limaVer,
	}
	b.adoptPendingSession()
	return SaveMeta(b.Meta)
}

// Drifted reports whether the current configuration would render a different
// VM than the one that exists (so the user knows a rebuild is needed).
func (b *Box) Drifted() (bool, error) {
	if b.Meta == nil {
		return false, nil
	}
	_, hash, err := b.RenderYAML()
	if err != nil {
		return false, err
	}
	return hash != b.Meta.TemplateHash, nil
}

// Touch records a session start.
func (b *Box) Touch() {
	if b.Meta == nil {
		return
	}
	b.Meta.LastUsed = time.Now()
	b.Meta.Sessions++
	_ = SaveMeta(b.Meta)
}

// Delete destroys the VM and metadata. Agent state (login) is kept.
func (b *Box) Delete(ctx context.Context, progress lima.Progress) error {
	StopBroker(b.Meta)
	if _, st, err := b.Status(ctx); err != nil {
		return err
	} else if st != StateMissing {
		if err := b.Lima.Delete(ctx, b.Name, progress); err != nil {
			return err
		}
	}
	if dir, err := paths.BoxesDir(); err == nil {
		_ = os.Remove(filepath.Join(dir, b.Name+".lima.yaml"))
	}
	b.Meta = nil
	return DeleteMeta(b.Name)
}

// ---------------------------------------------------------------------------
// Launch
// ---------------------------------------------------------------------------

// LaunchSpec is everything needed to exec into the box.
type LaunchSpec struct {
	Argv    []string          // command inside the box
	Env     map[string]string // forwarded as CORRAL_FWD_<K> over SSH SendEnv
	GitEnv  map[string]string // CORRAL_GIT_TOKEN_<HOST> / CORRAL_GIT_USER_<HOST>, sent as-is
	Workdir string
	// Forwarded lists the names (never values) of host variables that were
	// forwarded, for the audit log and `--dry-run`.
	Forwarded []string
	// Warnings collected while resolving (e.g. skipped variables).
	Warnings []string
}

// BuildLaunch resolves the environment for a run. hostEnv is normally
// os.Environ() as a map; it is injected for tests.
func (b *Box) BuildLaunch(ag agent.Agent, argv []string, yolo bool, hostEnv map[string]string) (*LaunchSpec, error) {
	cfg := b.Cfg
	spec := &LaunchSpec{Env: map[string]string{}, GitEnv: map[string]string{}, Workdir: b.GuestPath()}

	// env_file: consulted after the exported host environment and
	// before the Keychain, for hosts with no login session. From here on
	// hostEnv already contains its values; fromFile tags the audit entries.
	hostEnv, fromFile, err := withEnvFile(cfg, hostEnv)
	if err != nil {
		return nil, err
	}
	src := func(k string) string {
		if fromFile[k] {
			return k + "<-env_file"
		}
		return k
	}

	// Aliases fail closed and own their key.
	aliased := map[string]bool{}
	for _, e := range cfg.EnvFromHost {
		k, hostVar, _ := strings.Cut(e, "=")
		v, ok := hostEnv[hostVar]
		if !ok || v == "" {
			return nil, fmt.Errorf("env_from_host %s=%s: host variable %s is not set (refusing to start so a broader %s cannot leak in)", k, hostVar, hostVar, k)
		}
		spec.Env[k] = v
		aliased[k] = true
		spec.Forwarded = append(spec.Forwarded, k+"<-"+src(hostVar))
	}
	// keychain_env: forwarded like forward_env, but the value may come from the
	// macOS Keychain (generic password, service = variable name) so nothing has
	// to be exported in every shell. An exported variable wins; a missing item
	// refuses to start, like env_from_host — silently running without the
	// credential is what the user would least expect.
	for _, k := range cfg.KeychainEnv {
		if aliased[k] {
			continue
		}
		if v, ok := hostEnv[k]; ok && v != "" {
			spec.Env[k] = v
			spec.Forwarded = append(spec.Forwarded, src(k))
			continue
		}
		v, err := KeychainLookup(k)
		if err == nil && v == "" {
			err = errors.New("the Keychain item is empty")
		}
		if err != nil {
			return nil, fmt.Errorf("keychain_env %s: %w (%s) — refusing to start without it", k, err, KeychainRemedy(k, err))
		}
		spec.Env[k] = v
		spec.Forwarded = append(spec.Forwarded, k+"<-keychain")
	}
	// Explicit env: KEY=value verbatim, KEY forwards from host.
	for _, e := range cfg.Env {
		k, v, has := strings.Cut(e, "=")
		if aliased[k] {
			continue
		}
		if has {
			spec.Env[k] = v
			spec.Forwarded = append(spec.Forwarded, k)
			continue
		}
		if hv, ok := hostEnv[k]; ok {
			spec.Env[k] = hv
			spec.Forwarded = append(spec.Forwarded, src(k))
		} else {
			spec.Warnings = append(spec.Warnings, "env "+k+" is not set on the host; skipped")
		}
	}
	// Automatic passthrough of well-known credentials. When it is off (the
	// key, or the strict profile), a declared variable that the host has set
	// is dropped on purpose — but never silently: the agent would fail
	// with "not logged in" and point the user at authentication.
	if cfg.NoEnvPassthrough {
		spec.Warnings = append(spec.Warnings, suppressedForwardEnv(cfg, ag, hostEnv, spec.Env)...)
	}
	if !cfg.NoEnvPassthrough {
		keys := append([]string{}, cfg.ForwardEnv...)
		if ag != nil {
			keys = append(keys, ag.ForwardEnv()...)
		}
		for _, k := range keys {
			if aliased[k] {
				continue
			}
			if _, set := spec.Env[k]; set { // includes keychain_env

				continue
			}
			if v, ok := hostEnv[k]; ok && v != "" {
				spec.Env[k] = v
				spec.Forwarded = append(spec.Forwarded, src(k))
			}
		}
		for _, k := range []string{"TERM", "LANG", "LC_ALL", "COLORTERM", "TZ"} {
			if v, ok := hostEnv[k]; ok && v != "" {
				spec.Env[k] = v
			}
		}
	}
	// Git identity (name/email only).
	if cfg.GitIdentity {
		if name, email := hostGitIdentity(); name != "" || email != "" {
			if name != "" {
				spec.Env["GIT_AUTHOR_NAME"], spec.Env["GIT_COMMITTER_NAME"] = name, name
			}
			if email != "" {
				spec.Env["GIT_AUTHOR_EMAIL"], spec.Env["GIT_COMMITTER_EMAIL"] = email, email
			}
		}
	}
	// Git tokens per host. The variable is looked up in the session
	// environment built above first — env_from_host aliases, keychain_env and
	// env deliver values that were never exported on the host — then in
	// the host environment.
	for host, gt := range cfg.GitTokens {
		v, ok := spec.Env[gt.Token]
		if !ok || v == "" {
			v, ok = hostEnv[gt.Token]
		}
		if !ok || v == "" {
			spec.Warnings = append(spec.Warnings, fmt.Sprintf("git_tokens[%s]: variable %s is not set on the host (export it, put it in env_file, or list it in keychain_env); HTTPS pushes to %s will prompt", host, gt.Token, host))
			continue
		}
		spec.GitEnv["CORRAL_GIT_TOKEN_"+gitHostKey(host)] = v
		if gt.User != "" {
			spec.GitEnv["CORRAL_GIT_USER_"+gitHostKey(host)] = gt.User
		}
		spec.Forwarded = append(spec.Forwarded, "git:"+host+"<-"+src(gt.Token))
	}
	// Clone mode: the launcher clones inside the box at session start, when
	// the git_tokens credential is present. Refuse up front if it cannot.
	if cfg.Source == config.SourceClone {
		cs, err := b.cloneSpec()
		if err != nil {
			return nil, err
		}
		if _, ok := cfg.GitTokens[cs.Host]; !ok && !cfg.SSHAgent {
			return nil, fmt.Errorf("source = \"clone\" from %s needs a credential for that host: add git_tokens = { %q = \"<HOST_VAR>\" } to ~/.corral/config.toml (or ~/.corral/projects/%s.toml)", cs.Host, cs.Host, b.Name)
		}
		spec.Env["CORRAL_CLONE_URL"] = cs.URL
		spec.Env["CORRAL_CLONE_REF"] = cs.Ref
		spec.Forwarded = append(spec.Forwarded, "clone:"+cs.Host)
	}
	spec.Env["CORRAL_SOURCE"] = cfg.Source
	// Box identity for tools inside.
	spec.Env["CORRAL_NAME"] = b.Name
	// api_brokers: the base URL of each credential-holding route on the
	// Mac, e.g. CORRAL_API_GITLAB=http://192.168.5.2:42xxx/gitlab — the
	// token itself never travels.
	for _, ab := range cfg.APIBrokers {
		spec.Env["CORRAL_API_"+gitHostKey(ab.Name)] = APIBaseURL(b.Name, ab.Name)
		spec.Forwarded = append(spec.Forwarded, "api:"+ab.Name+"<-"+ab.Token)
	}
	// Every process of this session inherits the id; `run --timeout` uses it
	// to end the session inside the box when killing ssh alone would not
	// (no pty without a local TTY — the unattended case).
	spec.Env["CORRAL_SESSION"] = newSessionID()
	spec.Env["CORRAL_PROJECT"] = b.GuestPath()
	spec.Env["CORRAL_VERSION"] = b.Version
	if yolo {
		spec.Env["CORRAL_YOLO"] = "1"
	} else {
		spec.Env["CORRAL_YOLO"] = "0"
	}
	if ag != nil {
		spec.Env["CORRAL_AGENT"] = ag.Name()
		spec.Argv = ag.Argv(agent.LaunchOptions{Yolo: yolo, Args: argv, Interactive: true})
	} else {
		spec.Argv = argv
	}
	sort.Strings(spec.Forwarded)
	return spec, nil
}

func gitHostKey(host string) string {
	r := strings.NewReplacer(".", "_", ":", "_", "-", "_")
	return strings.ToUpper(r.Replace(host))
}

// ProcessEnv returns the environment to give the ssh process so that SendEnv
// carries the forwarded values.
func (s *LaunchSpec) ProcessEnv(base []string) []string {
	out := make([]string, 0, len(base)+len(s.Env)+len(s.GitEnv))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "CORRAL_FWD_") || strings.HasPrefix(k, "CORRAL_GIT_") {
			continue
		}
		out = append(out, kv)
	}
	for k, v := range s.Env {
		out = append(out, "CORRAL_FWD_"+k+"="+v)
	}
	for k, v := range s.GitEnv {
		out = append(out, k+"="+v)
	}
	return out
}

// SendEnvPatterns are the SSH SendEnv globs matching ProcessEnv output.
func SendEnvPatterns() []string { return []string{"CORRAL_FWD_*", "CORRAL_GIT_*"} }

// HostEnvMap converts os.Environ() into a map.
func HostEnvMap(environ []string) map[string]string {
	m := make(map[string]string, len(environ))
	for _, kv := range environ {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

// newSessionID is a random, URL-safe id for CORRAL_SESSION.
func newSessionID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

// suppressedForwardEnv names the variables no_env_passthrough kept from the
// box although the host has them: the user's own forward_env entries one by
// one, the agent's well-known credential variables as a group.
func suppressedForwardEnv(cfg *config.Config, ag agent.Agent, hostEnv, already map[string]string) []string {
	why := "no_env_passthrough = true"
	if cfg.Profile == config.ProfileStrict {
		why = "profile \"strict\" sets no_env_passthrough"
	}
	const remedy = "keychain_env, env_from_host, env, env_file or api_brokers forward it explicitly"
	var out []string
	for _, k := range cfg.ForwardEnv {
		if _, set := already[k]; set {
			continue
		}
		if v, ok := hostEnv[k]; ok && v != "" {
			out = append(out, fmt.Sprintf("forward_env %s is set on the host but not forwarded: %s — %s", k, why, remedy))
		}
	}
	if ag == nil {
		return out
	}
	var dropped []string
	for _, k := range ag.ForwardEnv() {
		if _, set := already[k]; set {
			continue
		}
		if v, ok := hostEnv[k]; ok && v != "" {
			dropped = append(dropped, k)
		}
	}
	if len(dropped) > 0 {
		out = append(out, fmt.Sprintf("%s is set on the host but not forwarded (%s); %s log in inside the box or %s", strings.Join(dropped, ", "), why, ag.Name(), remedy))
	}
	return out
}
