// Package config loads and merges Corral configuration.
//
// Precedence (highest wins): CLI flags > project .corral.toml > global
// ~/.corral/config.toml > built-in defaults. Scalar keys are overridden;
// list keys (packages, env, mounts, …) are unioned so a project can add to the
// global set without repeating it.
//
// This package only parses, merges and validates shapes. Which layer may set
// which key — the project file is written by whoever controls the repository
// and is not trusted with host access — is decided in internal/policy, whose
// Load is the entry point callers use.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// ProjectFileName is the per-project configuration file, looked up in the
// project root (the directory Corral is launched from).
const ProjectFileName = ".corral.toml"

// File mirrors the TOML schema. Pointer fields distinguish "unset" from the
// zero value so that layers can be merged.
type File struct {
	DefaultAgent       *string             `toml:"default_agent,omitempty"`
	CPUs               *int                `toml:"cpus,omitempty"`
	Memory             *string             `toml:"memory,omitempty"`
	Disk               *string             `toml:"disk,omitempty"`
	Yolo               *bool               `toml:"yolo,omitempty"`
	StopOnExit         *bool               `toml:"stop_on_exit,omitempty"`
	ReadonlyProject    *bool               `toml:"readonly_project,omitempty"`
	SharedAgentState   *bool               `toml:"shared_agent_state,omitempty"` // deprecated alias: true = agent_state "shared", false = "isolated"
	AgentState         *string             `toml:"agent_state,omitempty"`
	GitIdentity        *bool               `toml:"git_identity,omitempty"`
	SSHAgent           *bool               `toml:"ssh_agent,omitempty"`
	NoEnvPassthrough   *bool               `toml:"no_env_passthrough,omitempty"`
	ProtectGitMetadata *bool               `toml:"protect_git_metadata,omitempty"`
	Name               *string             `toml:"name,omitempty"`
	Network            *string             `toml:"network,omitempty"`
	Rosetta            *bool               `toml:"rosetta,omitempty"`
	IdleStop           *string             `toml:"idle_stop,omitempty"`
	Golden             *bool               `toml:"golden,omitempty"`
	Source             *string             `toml:"source,omitempty"`
	Snapshot           *string             `toml:"snapshot,omitempty"`
	SnapshotsKeep      *int                `toml:"snapshots_keep,omitempty"`
	Profile            *string             `toml:"profile,omitempty"`
	ForwardEnv         []string            `toml:"forward_env,omitempty"`
	Env                []string            `toml:"env,omitempty"`
	EnvFromHost        []string            `toml:"env_from_host,omitempty"`
	KeychainEnv        []string            `toml:"keychain_env,omitempty"`   // forwarded like forward_env, value read from the macOS Keychain (service = name) when not in the environment
	EnvFile            *string             `toml:"env_file,omitempty"`       // KEY=value file consulted after the host environment (unattended hosts); trusted layers only
	MaxRunning         *int                `toml:"max_running,omitempty"`    // admission: refuse to start a box when this many are already running (0 = no limit)
	MemoryReserve      *string             `toml:"memory_reserve,omitempty"` // admission: RAM that must stay free for macOS when a box starts
	Timeout            *string             `toml:"timeout,omitempty"`        // default `run --timeout`: bound a session, exit 124
	Packages           []string            `toml:"packages,omitempty"`
	Toolchains         []string            `toml:"toolchains,omitempty"`
	ToolchainVersions  map[string]string   `toml:"toolchain_versions,omitempty"` // { flutter = "3.44.2[@<commit>]" }; part of the golden identity
	Mounts             []string            `toml:"mounts,omitempty"`
	Provision          []string            `toml:"provision,omitempty"`
	Hide               []string            `toml:"hide,omitempty"`
	BoxDirs            []string            `toml:"box_dirs,omitempty"` // project dirs kept on the box disk (bind-mounted over the mount)
	Egress             []string            `toml:"egress,omitempty"`
	GitTokens          map[string]GitToken `toml:"git_tokens,omitempty"`
	APIBrokers         []APIBroker         `toml:"api_brokers,omitempty"` // credential-holding API proxies on the Mac; trusted layers only
}

// APIBroker is one credential-holding API proxy: the box reaches
// http://192.168.5.2:<broker port>/<name>/… ; the Mac adds the credential and
// forwards to upstream — the token never enters the box, and only the
// method+path patterns in allow get through.
//
//	[[api_brokers]]
//	name     = "gitlab"
//	upstream = "https://git.example.com"
//	token    = "GITLAB_TOKEN"                  # host variable: env, env_file or Keychain (keychain_env)
//	auth     = "header"                           # header (default) | bearer | basic
//	header   = "PRIVATE-TOKEN"                    # for auth = "header"
//	user     = "me@example.com"                   # for auth = "basic" (user:token)
//	allow    = ["GET /api/v4/projects/42/**", "POST /api/v4/projects/42/merge_requests/*/notes"]
type APIBroker struct {
	Name     string   `toml:"name"`
	Upstream string   `toml:"upstream"`
	Token    string   `toml:"token"`
	Auth     string   `toml:"auth,omitempty"`
	Header   string   `toml:"header,omitempty"`
	User     string   `toml:"user,omitempty"`
	Allow    []string `toml:"allow"`
}

// API auth styles.
const (
	APIAuthHeader = "header" // <Header>: <token>
	APIAuthBearer = "bearer" // Authorization: Bearer <token>
	APIAuthBasic  = "basic"  // Authorization: Basic base64(user:token)
)

var (
	apiNameRe  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	apiAllowRe = regexp.MustCompile(`^(\*|[A-Z]{3,10}) /[^ ?#]*$`)
	headerRe   = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
)

// GitToken is one git_tokens entry: the host variable holding the credential
// and, optionally, the username the guest credential helper sends. In TOML
// either form is accepted:
//
//	git_tokens = { "git.example.com" = "GITLAB_TOKEN" }                                   # user oauth2
//	git_tokens = { "git.example.com" = { token = "DEPLOY_TOKEN", user = "gitlab+deploy-token-1" } }
//
// GitLab personal access tokens authenticate as `oauth2`; a deploy token —
// the narrower credential — needs its own username.
type GitToken struct {
	Token string `toml:"token"`
	User  string `toml:"user,omitempty"`
}

// UnmarshalTOML accepts the bare-string and the table form.
func (g *GitToken) UnmarshalTOML(v any) error {
	switch t := v.(type) {
	case string:
		*g = GitToken{Token: t}
		return nil
	case map[string]any:
		out := GitToken{}
		for k, val := range t {
			s, ok := val.(string)
			if !ok {
				return fmt.Errorf("git_tokens %s must be a string", k)
			}
			switch k {
			case "token":
				out.Token = s
			case "user":
				out.User = s
			default:
				return fmt.Errorf("git_tokens: unknown field %q (token, user)", k)
			}
		}
		if out.Token == "" {
			return errors.New("git_tokens entry needs token = \"<HOST_VAR>\"")
		}
		*g = out
		return nil
	}
	return fmt.Errorf("git_tokens entry must be \"<HOST_VAR>\" or { token = \"<HOST_VAR>\", user = \"...\" }, got %T", v)
}

// String renders the entry for the resolved view: VAR or VAR (user).
func (g GitToken) String() string {
	if g.User != "" {
		return g.Token + " (" + g.User + ")"
	}
	return g.Token
}

// Config is the fully resolved configuration used by the rest of the program.
type Config struct {
	DefaultAgent       string
	CPUs               int
	Memory             string
	Disk               string
	Yolo               bool
	StopOnExit         bool
	ReadonlyProject    bool
	SharedAgentState   bool   // derived: AgentState == AgentStateShared
	AgentState         string // AgentStateShared | AgentStateSeeded | AgentStateIsolated
	GitIdentity        bool
	SSHAgent           bool
	NoEnvPassthrough   bool
	ProtectGitMetadata bool
	Name               string
	Network            string
	Rosetta            bool
	IdleStop           time.Duration // 0 = never
	Golden             bool          // clone new boxes from a golden image
	Source             string        // SourceMount | SourceClone
	Snapshot           string        // SnapshotOff | SnapshotAuto
	SnapshotsKeep      int           // automatic snapshots to keep per box
	Profile            string        // ProfileDefault | ProfileOffline | ProfileStrict (a floor, see policy.ApplyProfile)
	ForwardEnv         []string
	Env                []string
	EnvFromHost        []string
	KeychainEnv        []string
	EnvFile            string // absolute path or "", ~ expanded
	MaxRunning         int    // 0 = unlimited
	MemoryReserve      string
	Timeout            string // "" = none; a duration
	Packages           []string
	Toolchains         []string
	ToolchainVersions  map[string]string
	Mounts             []Mount
	Provision          []string
	Hide               []string
	BoxDirs            []string
	Egress             []string // network = "broker": allowed destinations, host or *.suffix, optional :port
	GitTokens          map[string]GitToken
	APIBrokers         []APIBroker

	// Sources records which files contributed, for `corral config`.
	Sources []string
}

// Mount is a parsed "host:guest[:ro]" mount specification.
type Mount struct {
	Host     string
	Guest    string
	Writable bool
}

// String renders the mount back into its config form.
func (m Mount) String() string {
	s := m.Host + ":" + m.Guest
	if !m.Writable {
		s += ":ro"
	}
	return s
}

// Network modes. Full is the default (outbound internet, like a container);
// Broker routes every connection through an allow-list proxy on the Mac and
// rejects everything else inside the guest; Offline rejects all egress. Both
// remove sudo so the agent cannot undo the rule — see docs/SECURITY.md.
const (
	NetworkFull    = "full"
	NetworkBroker  = "broker"
	NetworkOffline = "offline"
)

// Networks in loosest-to-strictest order.
var Networks = []string{NetworkFull, NetworkBroker, NetworkOffline}

// NetworkRank orders network modes for tighten-only comparison (-1 if unknown).
func NetworkRank(n string) int {
	for i, k := range Networks {
		if k == n {
			return i
		}
	}
	return -1
}

// DefaultEgress is the allow-list every broker box starts from: the agents'
// API hosts. Hosts named in git_tokens are added automatically.
// platform.claude.com is where Claude Code refreshes its OAuth access token;
// without it a subscription login works until the first token expiry and then
// fails with "Re-authenticate to continue" inside every broker box.
var DefaultEgress = []string{"api.anthropic.com", "*.anthropic.com", "platform.claude.com"}

// Snapshot modes. Auto takes a copy-on-write snapshot of the box disk at
// session start when the box is stopped (the normal case with idle_stop),
// keeping the last SnapshotsKeep; `corral undo` restores the newest.
const (
	SnapshotOff  = "off"
	SnapshotAuto = "auto"
)

// Agent state modes: where an agent's login/settings live for a box.
// Shared mounts ~/.corral/agents/<agent> read-write into the box (one
// login for all boxes; any box can read or overwrite it). Seeded copies that
// directory into the box's own disk at first boot, then the box is on its own
// (an overwrite stays local; the token copy is still readable). Isolated
// starts empty — log in once per box.
const (
	AgentStateShared   = "shared"
	AgentStateSeeded   = "seeded"
	AgentStateIsolated = "isolated"
)

// AgentStates in loosest-to-strictest order.
var AgentStates = []string{AgentStateShared, AgentStateSeeded, AgentStateIsolated}

// AgentStateRank orders modes for tighten-only comparison (-1 if unknown).
func AgentStateRank(m string) int {
	for i, k := range AgentStates {
		if k == m {
			return i
		}
	}
	return -1
}

// agentStateOf resolves the mode from the new key, falling back to the
// deprecated boolean.
func agentStateOf(f File) string {
	if f.AgentState != nil {
		return *f.AgentState
	}
	if f.SharedAgentState != nil && !*f.SharedAgentState {
		return AgentStateIsolated
	}
	return AgentStateShared
}

// Source modes: how the project reaches the box (see internal/box/source.go).
const (
	SourceMount = "mount"
	SourceClone = "clone"
)

// Profiles name a bundle of guarantees that internal/policy expands and
// enforces as a floor: keys may tighten beyond a profile, never loosen below it.
const (
	ProfileDefault = "default"
	ProfileOffline = "offline"
	ProfileStrict  = "strict"
)

// Profiles in loosest-to-strictest order.
var Profiles = []string{ProfileDefault, ProfileOffline, ProfileStrict}

// ProfileRank orders profiles for tighten-only comparison (-1 if unknown).
func ProfileRank(p string) int {
	for i, k := range Profiles {
		if k == p {
			return i
		}
	}
	return -1
}

// KnownToolchains lists the guest toolchains Corral knows how to install.
var KnownToolchains = []string{"node", "go", "python", "docker", "java", "android", "flutter"}

// VersionedToolchains are the toolchains whose version `toolchain_versions`
// may pin. The others install a release the script pins and verifies.
var VersionedToolchains = []string{"flutter"}

// toolchainVersionRe: a release like 3.44.2, optionally @<40-hex commit>.
var toolchainVersionRe = regexp.MustCompile(`^[0-9][A-Za-z0-9.+-]{0,63}(@[0-9a-f]{40})?$`)

// SplitToolchainVersion separates "3.44.2@<commit>" into its parts.
func SplitToolchainVersion(v string) (version, commit string) {
	version, commit, _ = strings.Cut(v, "@")
	return version, commit
}

// Defaults are the built-in values before any file is read.
func Defaults() File {
	return File{
		DefaultAgent:       ptr("claude"),
		CPUs:               ptr(4),
		Memory:             ptr("4GiB"), // measured, see docs/FEASIBILITY.md §4; docker toolchain: 6GiB
		Disk:               ptr("60GiB"),
		Yolo:               ptr(true),
		StopOnExit:         ptr(false),
		ReadonlyProject:    ptr(false),
		SharedAgentState:   ptr(true),
		GitIdentity:        ptr(true),
		SSHAgent:           ptr(false),
		NoEnvPassthrough:   ptr(false),
		Network:            ptr(NetworkFull),
		Rosetta:            ptr(false),
		IdleStop:           ptr("30m"),
		Golden:             ptr(true),
		Source:             ptr(SourceMount),
		Snapshot:           ptr(SnapshotOff),
		SnapshotsKeep:      ptr(3),
		Profile:            ptr(ProfileDefault),
		ProtectGitMetadata: ptr(true),
		MemoryReserve:      ptr("8GiB"), // what doctor's "Box budget" has always kept for macOS
		ForwardEnv:         []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"},
		Egress:             DefaultEgress,
		Toolchains:         []string{"node"},
	}
}

func ptr[T any](v T) *T { return &v }

// ReadFile parses one TOML layer. ok is false when the file does not exist.
// Unknown keys are an error so typos cannot silently disable a setting.
func ReadFile(path string) (File, bool, error) {
	var f File
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return f, false, nil
	}
	if err != nil {
		return f, false, fmt.Errorf("read %s: %w", path, err)
	}
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return f, false, fmt.Errorf("parse %s: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			keys = append(keys, k.String())
		}
		return f, false, fmt.Errorf("%s: unknown key(s): %s", path, strings.Join(keys, ", "))
	}
	return f, true, nil
}

// Merge overlays b on top of a. Scalars from b win when set; lists are unioned
// preserving order; maps are merged with b winning on key conflicts.
func Merge(a, b File) File {
	out := a
	if b.DefaultAgent != nil {
		out.DefaultAgent = b.DefaultAgent
	}
	if b.CPUs != nil {
		out.CPUs = b.CPUs
	}
	if b.Memory != nil {
		out.Memory = b.Memory
	}
	if b.Disk != nil {
		out.Disk = b.Disk
	}
	if b.Yolo != nil {
		out.Yolo = b.Yolo
	}
	if b.StopOnExit != nil {
		out.StopOnExit = b.StopOnExit
	}
	if b.ReadonlyProject != nil {
		out.ReadonlyProject = b.ReadonlyProject
	}
	if b.SharedAgentState != nil {
		out.SharedAgentState = b.SharedAgentState
		// The alias and the key describe one setting: a layer that writes
		// the alias supersedes an earlier layer's agent_state.
		if b.AgentState == nil {
			out.AgentState = nil
		}
	}
	if b.AgentState != nil {
		out.AgentState = b.AgentState
	}
	if b.GitIdentity != nil {
		out.GitIdentity = b.GitIdentity
	}
	if b.SSHAgent != nil {
		out.SSHAgent = b.SSHAgent
	}
	if b.NoEnvPassthrough != nil {
		out.NoEnvPassthrough = b.NoEnvPassthrough
	}
	if b.ProtectGitMetadata != nil {
		out.ProtectGitMetadata = b.ProtectGitMetadata
	}
	if b.Name != nil {
		out.Name = b.Name
	}
	if b.Network != nil {
		out.Network = b.Network
	}
	if b.Rosetta != nil {
		out.Rosetta = b.Rosetta
	}
	if b.IdleStop != nil {
		out.IdleStop = b.IdleStop
	}
	if b.Golden != nil {
		out.Golden = b.Golden
	}
	if b.Source != nil {
		out.Source = b.Source
	}
	if b.Profile != nil {
		out.Profile = b.Profile
	}
	if b.Snapshot != nil {
		out.Snapshot = b.Snapshot
	}
	if b.SnapshotsKeep != nil {
		out.SnapshotsKeep = b.SnapshotsKeep
	}
	out.ForwardEnv = union(a.ForwardEnv, b.ForwardEnv)
	out.Env = union(a.Env, b.Env)
	out.EnvFromHost = union(a.EnvFromHost, b.EnvFromHost)
	out.KeychainEnv = union(a.KeychainEnv, b.KeychainEnv)
	if b.EnvFile != nil {
		out.EnvFile = b.EnvFile
	}
	if b.MaxRunning != nil {
		out.MaxRunning = b.MaxRunning
	}
	if b.MemoryReserve != nil {
		out.MemoryReserve = b.MemoryReserve
	}
	if b.Timeout != nil {
		out.Timeout = b.Timeout
	}
	out.Packages = union(a.Packages, b.Packages)
	// toolchains is what gets installed, not a restriction: an explicitly
	// empty list in a layer (`toolchains = []`) replaces what the layers below
	// asked for, so a project can opt out of the default node. nil is
	// "unset" and keeps the union like every other list.
	if b.Toolchains != nil && len(b.Toolchains) == 0 {
		out.Toolchains = []string{}
	} else {
		out.Toolchains = union(a.Toolchains, b.Toolchains)
	}
	if len(a.ToolchainVersions) > 0 || len(b.ToolchainVersions) > 0 {
		out.ToolchainVersions = map[string]string{}
		for k, v := range a.ToolchainVersions {
			out.ToolchainVersions[k] = v
		}
		for k, v := range b.ToolchainVersions {
			out.ToolchainVersions[k] = v
		}
	}
	out.Mounts = union(a.Mounts, b.Mounts)
	out.Provision = union(a.Provision, b.Provision)
	out.Hide = union(a.Hide, b.Hide)
	out.BoxDirs = union(a.BoxDirs, b.BoxDirs)
	out.Egress = union(a.Egress, b.Egress)
	// api_brokers: later layers override by name, never remove.
	if len(a.APIBrokers) > 0 || len(b.APIBrokers) > 0 {
		out.APIBrokers = nil
		byName := map[string]int{}
		for _, ab := range a.APIBrokers {
			byName[ab.Name] = len(out.APIBrokers)
			out.APIBrokers = append(out.APIBrokers, ab)
		}
		for _, ab := range b.APIBrokers {
			if i, ok := byName[ab.Name]; ok {
				out.APIBrokers[i] = ab
				continue
			}
			byName[ab.Name] = len(out.APIBrokers)
			out.APIBrokers = append(out.APIBrokers, ab)
		}
	}
	if len(a.GitTokens) > 0 || len(b.GitTokens) > 0 {
		out.GitTokens = map[string]GitToken{}
		for k, v := range a.GitTokens {
			out.GitTokens[k] = v
		}
		for k, v := range b.GitTokens {
			out.GitTokens[k] = v
		}
	}
	return out
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]string{}, a...), b...) {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

var (
	sizeRe   = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?(GiB|MiB|G|M|GB|MB)$`)
	envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// EnvKeyRe is envKeyRe for other packages that validate variable names.
	EnvKeyRe  = envKeyRe
	nameRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)
	hostKeyRe = regexp.MustCompile(`^[A-Za-z0-9.-]+(:[0-9]+)?$`)
	// gitUserRe bounds the username the guest credential helper sends (it
	// travels as an SSH environment value and is printed into a credential
	// response).
	gitUserRe = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,128}$`)
	egressRe  = regexp.MustCompile(`^(\*\.)?[A-Za-z0-9][A-Za-z0-9.-]*(:[0-9]{1,5})?$`)
)

// Resolve validates a merged File and turns it into a Config.
func Resolve(f File) (*Config, error) {
	c := &Config{
		DefaultAgent:       deref(f.DefaultAgent),
		CPUs:               deref(f.CPUs),
		Memory:             deref(f.Memory),
		Disk:               deref(f.Disk),
		Yolo:               deref(f.Yolo),
		StopOnExit:         deref(f.StopOnExit),
		ReadonlyProject:    deref(f.ReadonlyProject),
		AgentState:         agentStateOf(f),
		GitIdentity:        deref(f.GitIdentity),
		SSHAgent:           deref(f.SSHAgent),
		NoEnvPassthrough:   deref(f.NoEnvPassthrough),
		ProtectGitMetadata: deref(f.ProtectGitMetadata),
		Name:               deref(f.Name),
		Network:            deref(f.Network),
		Rosetta:            deref(f.Rosetta),
		Golden:             deref(f.Golden),
		Source:             deref(f.Source),
		Profile:            deref(f.Profile),
		Snapshot:           deref(f.Snapshot),
		SnapshotsKeep:      deref(f.SnapshotsKeep),
		ForwardEnv:         f.ForwardEnv,
		Env:                f.Env,
		EnvFromHost:        f.EnvFromHost,
		KeychainEnv:        f.KeychainEnv,
		EnvFile:            deref(f.EnvFile),
		MaxRunning:         deref(f.MaxRunning),
		MemoryReserve:      deref(f.MemoryReserve),
		Timeout:            deref(f.Timeout),
		Packages:           f.Packages,
		Toolchains:         f.Toolchains,
		ToolchainVersions:  f.ToolchainVersions,
		Provision:          f.Provision,
		Hide:               f.Hide,
		BoxDirs:            f.BoxDirs,
		Egress:             f.Egress,
		GitTokens:          f.GitTokens,
		APIBrokers:         f.APIBrokers,
	}
	if c.CPUs < 1 {
		return nil, fmt.Errorf("cpus must be >= 1, got %d", c.CPUs)
	}
	if !sizeRe.MatchString(c.Memory) {
		return nil, fmt.Errorf("memory %q must look like 8GiB or 512MiB", c.Memory)
	}
	if !sizeRe.MatchString(c.MemoryReserve) {
		return nil, fmt.Errorf("memory_reserve %q must look like 8GiB or 512MiB", c.MemoryReserve)
	}
	if c.MaxRunning < 0 {
		return nil, fmt.Errorf("max_running %d must be 0 (no limit) or positive", c.MaxRunning)
	}
	if _, err := ParseTimeout(c.Timeout); err != nil {
		return nil, err
	}
	if !sizeRe.MatchString(c.Disk) {
		return nil, fmt.Errorf("disk %q must look like 60GiB", c.Disk)
	}
	idle, err := ParseIdleStop(deref(f.IdleStop))
	if err != nil {
		return nil, err
	}
	c.IdleStop = idle
	if c.Source != SourceMount && c.Source != SourceClone {
		return nil, fmt.Errorf("source %q must be %q or %q", c.Source, SourceMount, SourceClone)
	}
	if AgentStateRank(c.AgentState) < 0 {
		return nil, fmt.Errorf("agent_state %q must be one of %s", c.AgentState, strings.Join(AgentStates, ", "))
	}
	c.SharedAgentState = c.AgentState == AgentStateShared
	if c.Snapshot != SnapshotOff && c.Snapshot != SnapshotAuto {
		return nil, fmt.Errorf("snapshot %q must be %q or %q", c.Snapshot, SnapshotOff, SnapshotAuto)
	}
	if c.SnapshotsKeep < 1 || c.SnapshotsKeep > 50 {
		return nil, fmt.Errorf("snapshots_keep must be 1..50, got %d", c.SnapshotsKeep)
	}
	if ProfileRank(c.Profile) < 0 {
		return nil, fmt.Errorf("profile %q must be one of %s", c.Profile, strings.Join(Profiles, ", "))
	}
	if NetworkRank(c.Network) < 0 {
		return nil, fmt.Errorf("network %q must be one of %s", c.Network, strings.Join(Networks, ", "))
	}
	for _, e := range c.Egress {
		if !egressRe.MatchString(e) {
			return nil, fmt.Errorf("egress entry %q must be a hostname or *.suffix, optionally :port", e)
		}
	}
	if c.Name != "" && !nameRe.MatchString(c.Name) {
		return nil, fmt.Errorf("name %q must be lowercase letters, digits and dashes (max 31 chars)", c.Name)
	}
	for _, k := range c.ForwardEnv {
		if !envKeyRe.MatchString(k) {
			return nil, fmt.Errorf("forward_env entry %q is not a valid variable name", k)
		}
	}
	for _, e := range c.Env {
		k, _, _ := strings.Cut(e, "=")
		if !envKeyRe.MatchString(k) {
			return nil, fmt.Errorf("env entry %q must be KEY=value or KEY", e)
		}
	}
	envKeys := map[string]bool{}
	for _, e := range c.Env {
		k, _, _ := strings.Cut(e, "=")
		envKeys[k] = true
	}
	for _, e := range c.EnvFromHost {
		k, v, ok := strings.Cut(e, "=")
		if !ok || !envKeyRe.MatchString(k) || !envKeyRe.MatchString(v) {
			return nil, fmt.Errorf("env_from_host entry %q must be GUEST_VAR=HOST_VAR (plain names, no $)", e)
		}
		if envKeys[k] {
			return nil, fmt.Errorf("%s is set in both env and env_from_host; pick one", k)
		}
	}
	if c.EnvFile != "" {
		p := expandHome(c.EnvFile)
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("env_file %q must be an absolute path (or ~/...)", c.EnvFile)
		}
		c.EnvFile = p
	}
	for _, k := range c.KeychainEnv {
		if !envKeyRe.MatchString(k) {
			return nil, fmt.Errorf("keychain_env entry %q must be a plain variable name (the Keychain item's service name)", k)
		}
	}
	for _, t := range c.Toolchains {
		if !contains(KnownToolchains, t) {
			return nil, fmt.Errorf("unknown toolchain %q (known: %s)", t, strings.Join(KnownToolchains, ", "))
		}
	}
	for tc, v := range c.ToolchainVersions {
		if !contains(VersionedToolchains, tc) {
			return nil, fmt.Errorf("toolchain_versions[%q]: only %s can be pinned (the other toolchains install a release the script pins and verifies)", tc, strings.Join(VersionedToolchains, ", "))
		}
		if !toolchainVersionRe.MatchString(v) {
			return nil, fmt.Errorf("toolchain_versions[%q] = %q must be a release like 3.44.2, optionally @<40-hex commit>", tc, v)
		}
	}
	seenAPI := map[string]bool{}
	for i := range c.APIBrokers {
		ab := &c.APIBrokers[i]
		if !apiNameRe.MatchString(ab.Name) {
			return nil, fmt.Errorf("api_brokers[%d].name %q: lowercase letters, digits and - (starts with a letter)", i, ab.Name)
		}
		if seenAPI[ab.Name] {
			return nil, fmt.Errorf("api_brokers: name %q given twice", ab.Name)
		}
		seenAPI[ab.Name] = true
		u, err := url.Parse(ab.Upstream)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
			return nil, fmt.Errorf("api_brokers[%s].upstream %q must be https://host[:port] with no path, query or user", ab.Name, ab.Upstream)
		}
		if !envKeyRe.MatchString(ab.Token) {
			return nil, fmt.Errorf("api_brokers[%s].token %q must be a host environment variable name", ab.Name, ab.Token)
		}
		if ab.Auth == "" {
			ab.Auth = APIAuthHeader
		}
		switch ab.Auth {
		case APIAuthHeader:
			if ab.Header == "" {
				return nil, fmt.Errorf("api_brokers[%s]: auth = \"header\" needs header = \"<Name>\" (e.g. PRIVATE-TOKEN)", ab.Name)
			}
			if !headerRe.MatchString(ab.Header) || strings.EqualFold(ab.Header, "host") {
				return nil, fmt.Errorf("api_brokers[%s].header %q is not a valid header name", ab.Name, ab.Header)
			}
		case APIAuthBearer:
		case APIAuthBasic:
			if ab.User == "" || strings.ContainsAny(ab.User, ":\r\n") {
				return nil, fmt.Errorf("api_brokers[%s]: auth = \"basic\" needs user = \"<login>\" without ':'", ab.Name)
			}
		default:
			return nil, fmt.Errorf("api_brokers[%s].auth %q must be header, bearer or basic", ab.Name, ab.Auth)
		}
		if len(ab.Allow) == 0 {
			return nil, fmt.Errorf("api_brokers[%s]: allow must list at least one \"METHOD /path\" pattern (\"GET /api/v4/projects/42/**\")", ab.Name)
		}
		for _, a := range ab.Allow {
			if !apiAllowRe.MatchString(a) {
				return nil, fmt.Errorf("api_brokers[%s].allow %q must be \"METHOD /path\" (METHOD upper-case or *; path may use * for one segment and ** for the rest)", ab.Name, a)
			}
		}
	}
	if len(c.APIBrokers) > 0 && c.Network == NetworkOffline {
		return nil, errors.New("api_brokers cannot be used with network = \"offline\" (the box may not reach the Mac at all)")
	}
	for host, v := range c.GitTokens {
		if !hostKeyRe.MatchString(host) {
			return nil, fmt.Errorf("git_tokens key %q must be a hostname", host)
		}
		if !envKeyRe.MatchString(v.Token) {
			return nil, fmt.Errorf("git_tokens[%q] = %q must be a host environment variable name", host, v.Token)
		}
		if v.User != "" && !gitUserRe.MatchString(v.User) {
			return nil, fmt.Errorf("git_tokens[%q].user = %q: letters, digits and . _ + - only", host, v.User)
		}
	}
	for _, m := range f.Mounts {
		pm, err := ParseMount(m)
		if err != nil {
			return nil, err
		}
		c.Mounts = append(c.Mounts, pm)
	}
	sort.Strings(c.Packages)
	return c, nil
}

// ParseIdleStop parses idle_stop: a Go duration of at least one minute
// ("30m", "2h"), or "off"/"0"/"" to never stop a box for being idle.
// ParseTimeout parses the `timeout` key / `--timeout` flag: "" or "off" is
// none, otherwise a duration of at least one minute.
func ParseTimeout(s string) (time.Duration, error) {
	switch strings.TrimSpace(s) {
	case "", "0", "off", "none":
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("timeout %q must be a duration like 45m or 2h", s)
	}
	if d < time.Minute {
		return 0, fmt.Errorf("timeout %q must be at least 1m", s)
	}
	return d, nil
}

func ParseIdleStop(s string) (time.Duration, error) {
	switch strings.TrimSpace(s) {
	case "", "0", "off", "never", "false":
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("idle_stop %q must be a duration like 30m or 2h, or \"off\"", s)
	}
	if d < time.Minute {
		return 0, fmt.Errorf("idle_stop %q must be at least 1m (or \"off\")", s)
	}
	return d, nil
}

// ParseMount parses "host:guest[:ro|:rw]". A bare "host" mounts at the same
// path in the guest. "~" is expanded on the host side.
func ParseMount(spec string) (Mount, error) {
	parts := strings.Split(spec, ":")
	if len(parts) == 0 || len(parts) > 3 || parts[0] == "" {
		return Mount{}, fmt.Errorf("mount %q must be host:guest[:ro]", spec)
	}
	host := expandHome(parts[0])
	if !filepath.IsAbs(host) {
		return Mount{}, fmt.Errorf("mount %q: host path must be absolute or start with ~", spec)
	}
	guest := host
	if len(parts) >= 2 && parts[1] != "" {
		guest = parts[1]
	}
	if !strings.HasPrefix(guest, "/") {
		return Mount{}, fmt.Errorf("mount %q: guest path must be absolute", spec)
	}
	writable := true
	if len(parts) == 3 {
		switch parts[2] {
		case "ro":
			writable = false
		case "rw":
		default:
			return Mount{}, fmt.Errorf("mount %q: mode must be ro or rw", spec)
		}
	}
	return Mount{Host: filepath.Clean(host), Guest: filepath.Clean(guest), Writable: writable}, nil
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// WriteGlobal serialises f to path, creating parent directories.
func WriteGlobal(path string, f File) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var sb strings.Builder
	sb.WriteString("# Corral global configuration — see `corral config --help`.\n")
	sb.WriteString("# Project overrides live in .corral.toml at the project root.\n\n")
	enc := toml.NewEncoder(&sb)
	if err := enc.Encode(f); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	return os.WriteFile(path, []byte(sb.String()), 0o600)
}
