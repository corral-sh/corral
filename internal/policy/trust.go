// Package policy is the single place that decides what configuration Corral
// Box will act on. It answers one question for a reviewer: "what can a
// repository make us do?" — by classifying config keys by trust (trust.go),
// confining paths the repository supplies (paths.go) and refusing host
// locations whose exposure would defeat the sandbox.
//
// config parses and merges; box renders; policy sits between them. Nothing in
// box or cli should apply a rule that is not written here.
package policy

import (
	"fmt"
	"os"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/corral-sh/corral/internal/config"
)

// Trust says what a repository's .corral.toml may do with a key.
//
// The invariant behind it: project configuration is written by whoever
// controls the repository we are about to sandbox, so it must never be able to
// widen what the box can reach on the host. Everything that grants host access
// (credentials, mounts, agent forwarding, box identity) is TrustedOnly and
// belongs in a file the user owns.
type Trust int

const (
	// ProjectOK: the repository may set it freely. Guest-only effect.
	ProjectOK Trust = iota
	// ProjectTighten: the repository may only make the box *more* restrictive.
	ProjectTighten
	// TrustedOnly: refused in the project file; global config (or CLI) only.
	TrustedOnly
)

func (t Trust) String() string {
	switch t {
	case ProjectOK:
		return "project-ok"
	case ProjectTighten:
		return "project-may-tighten"
	default:
		return "trusted-only"
	}
}

// trustTable classifies every TOML key of config.File. TestTrustTableCoversFile fails
// when a key is added to File without an entry here — that is deliberate.
var trustTable = map[string]Trust{
	"default_agent":        ProjectOK,
	"cpus":                 ProjectOK, // capped against the host, see checkProjectResources
	"memory":               ProjectOK, // capped
	"disk":                 ProjectOK,
	"stop_on_exit":         ProjectOK,
	"packages":             ProjectOK,
	"toolchains":           ProjectOK,
	"toolchain_versions":   ProjectOK, // which release of a toolchain is installed in the guest; nothing on the Mac changes
	"provision":            ProjectOK, // must resolve inside the project; enforced in box.Render
	"hide":                 ProjectOK, // guest-side shadow of project paths; a repo can only hide more. HidePath confines it
	"box_dirs":             ProjectOK, // guest-side: listed project dirs live on the VM disk; BoxDirPath confines it to the project
	"rosetta":              ProjectOK, // guest-only: amd64 binaries/containers run inside the arm64 box
	"idle_stop":            ProjectOK, // only decides when this project's own box is stopped
	"snapshot":             ProjectOK, // copies of this project's own VM disk on the host; costs disk, exposes nothing
	"snapshots_keep":       ProjectOK,
	"golden":               ProjectOK, // how the box is built (clone vs from scratch); same result either way
	"yolo":                 ProjectTighten,
	"readonly_project":     ProjectTighten,
	"shared_agent_state":   ProjectTighten, // deprecated alias of agent_state
	"agent_state":          ProjectTighten, // shared → seeded → isolated; a repo may only ask for less of your login
	"git_identity":         ProjectTighten,
	"no_env_passthrough":   ProjectTighten,
	"protect_git_metadata": ProjectTighten, // a repo may not expose its own hooks/config to the host
	"network":              ProjectTighten, // a repo may go offline, never online
	"profile":              ProjectTighten, // a repo may pick a stricter bundle, never a looser one (see profile.go)
	"source":               ProjectTighten, // a repo may ask to be cloned in, never to be mounted
	"env":                  ProjectTighten, // literal KEY=value only; bare KEY forwards a host value
	"ssh_agent":            TrustedOnly,
	"name":                 TrustedOnly, // could steer the launch into another project's box
	"forward_env":          TrustedOnly,
	"env_from_host":        TrustedOnly,
	"keychain_env":         TrustedOnly, // a repo naming a Keychain item would pull a host secret into the box
	"env_file":             TrustedOnly, // a repo pointing the launcher at a file on the Mac would read host secrets
	"max_running":          TrustedOnly, // admission is the host owner's policy, not the repository's
	"memory_reserve":       TrustedOnly,
	"timeout":              TrustedOnly, // a repository must not be able to cut your session short
	"mounts":               TrustedOnly,
	"git_tokens":           TrustedOnly,
	"egress":               TrustedOnly, // widens where the box may connect; a repo may ask in its README, never grant
	"api_brokers":          TrustedOnly, // hands the box the use of a host credential; only its owner may grant that
}

// TrustOf returns the class of a TOML key (ok=false for unknown keys).
func TrustOf(key string) (Trust, bool) {
	t, ok := trustTable[key]
	return t, ok
}

// FileKeys lists every TOML key of config.File, sorted.
func FileKeys() []string { return fileKeys() }

// TrustedOnlyKeys lists the keys a repository may never set, for help text.
func TrustedOnlyKeys() []string { return keysWith(TrustedOnly) }

// TightenOnlyKeys lists the keys a repository may only tighten.
func TightenOnlyKeys() []string { return keysWith(ProjectTighten) }

func keysWith(t Trust) []string {
	var out []string
	for k, v := range trustTable {
		if v == t {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// fileKeys returns every toml key declared on config.File, via struct tags.
func fileKeys() []string {
	var out []string
	rt := reflect.TypeOf(config.File{})
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("toml")
		if name, _, _ := strings.Cut(tag, ","); name != "" && name != "-" {
			out = append(out, name)
		}
	}
	return out
}

// ProjectPolicyError is returned when a repository's .corral.toml tries to
// use a key it is not trusted with. Violations are collected so the user sees
// all of them at once; the box does not start.
type ProjectPolicyError struct {
	Path       string
	Violations []string
}

func (e *ProjectPolicyError) Error() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s is not trusted to:\n", e.Path)
	for _, v := range e.Violations {
		sb.WriteString("  - " + v + "\n")
	}
	sb.WriteString("A repository's .corral.toml can only shape the guest, never widen what the box reaches on your Mac.\n")
	sb.WriteString("Move these keys to ~/.corral/config.toml (yours), or remove them from the project file.")
	return sb.String()
}

// checkProjectFile enforces trustTable on the project layer. global is the
// already-merged lower layers, needed for tighten-only comparisons.
func checkProjectFile(path string, global, p config.File) error {
	var v []string
	// Trusted-only keys: any presence is a violation.
	if p.SSHAgent != nil {
		v = append(v, "set ssh_agent — it forwards your SSH agent into the box")
	}
	if p.Name != nil {
		v = append(v, "set name — it selects which box (and whose project mount) the session enters")
	}
	if len(p.ForwardEnv) > 0 {
		v = append(v, "set forward_env — it forwards host variables by name: "+strings.Join(p.ForwardEnv, ", "))
	}
	if len(p.EnvFromHost) > 0 {
		v = append(v, "set env_from_host — it forwards host variables: "+strings.Join(p.EnvFromHost, ", "))
	}
	if len(p.KeychainEnv) > 0 {
		v = append(v, "set keychain_env — it reads secrets from your Keychain: "+strings.Join(p.KeychainEnv, ", "))
	}
	if len(p.Mounts) > 0 {
		v = append(v, "add mounts — they expose host paths: "+strings.Join(p.Mounts, ", "))
	}
	if len(p.GitTokens) > 0 {
		hosts := make([]string, 0, len(p.GitTokens))
		for h := range p.GitTokens {
			hosts = append(hosts, h)
		}
		sort.Strings(hosts)
		v = append(v, "set git_tokens — it decides which host receives your token: "+strings.Join(hosts, ", "))
	}
	// Tighten-only booleans.
	v = appendTighten(v, "yolo", global.Yolo, p.Yolo, false)
	v = appendTighten(v, "readonly_project", global.ReadonlyProject, p.ReadonlyProject, true)
	v = appendTighten(v, "shared_agent_state", global.SharedAgentState, p.SharedAgentState, false)
	if p.AgentState != nil && config.AgentStateRank(*p.AgentState) < config.AgentStateRank(agentStateOf(global)) {
		v = append(v, fmt.Sprintf("set agent_state = %q — a project may only tighten it beyond %q (%s)", *p.AgentState, agentStateOf(global), strings.Join(config.AgentStates, " → ")))
	}
	v = appendTighten(v, "git_identity", global.GitIdentity, p.GitIdentity, false)
	v = appendTighten(v, "no_env_passthrough", global.NoEnvPassthrough, p.NoEnvPassthrough, true)
	v = appendTighten(v, "protect_git_metadata", global.ProtectGitMetadata, p.ProtectGitMetadata, true)
	if p.Source != nil && *p.Source != config.SourceClone && (global.Source == nil || *global.Source != *p.Source) {
		v = append(v, fmt.Sprintf("set source = %q — a project may only set it to %q", *p.Source, config.SourceClone))
	}
	if p.Network != nil && global.Network != nil && config.NetworkRank(*p.Network) < config.NetworkRank(*global.Network) {
		v = append(v, fmt.Sprintf("set network = %q — a project may only tighten it beyond %q (%s)", *p.Network, *global.Network, strings.Join(config.Networks, " → ")))
	}
	if len(p.Egress) > 0 {
		v = append(v, "set egress — it decides which hosts the box may reach: "+strings.Join(p.Egress, ", ")+" (copy the hosts into ~/.corral/projects/<box>.toml if you agree)")
	}
	if p.Profile != nil && (global.Profile == nil || config.ProfileRank(*p.Profile) < config.ProfileRank(*global.Profile)) {
		v = append(v, fmt.Sprintf("set profile = %q — a project may only choose a stricter profile than %q", *p.Profile, deref(global.Profile)))
	}
	// env: literal values only.
	for _, e := range p.Env {
		if !strings.Contains(e, "=") {
			v = append(v, fmt.Sprintf("forward host variable %s via env — use env = [%q] for a literal, or put the forward in your global config", e, e+"=value"))
		}
	}
	// Resources: a repository may ask, within what the host has.
	v = append(v, checkProjectResources(p)...)
	if len(v) > 0 {
		return &ProjectPolicyError{Path: path, Violations: v}
	}
	return nil
}

// agentStateOf mirrors config's resolution of the key + deprecated alias.
func agentStateOf(f config.File) string {
	if f.AgentState != nil {
		return *f.AgentState
	}
	if f.SharedAgentState != nil && !*f.SharedAgentState {
		return config.AgentStateIsolated
	}
	return config.AgentStateShared
}

func deref(s *string) string {
	if s == nil {
		return config.ProfileDefault
	}
	return *s
}

// appendTighten records a violation when the project sets a boolean to the
// loosening value. safe is the value that makes the box more restrictive.
func appendTighten(v []string, key string, global, project *bool, safe bool) []string {
	if project == nil || *project == safe {
		return v
	}
	if global != nil && *global == *project {
		return v // no change from what the user already chose
	}
	return append(v, fmt.Sprintf("set %s = %t — a project may only set it to %t", key, *project, safe))
}

// HostMemoryBytes is the Mac's physical RAM (0 if unknown); exported for
// `doctor` to print the box budget.
func HostMemoryBytes() uint64 { return hostMemoryBytes() }

// checkProjectResources caps cpus/memory requested by a project against the
// host so a repository cannot make the box unrunnable or starve the Mac.
func checkProjectResources(p config.File) []string {
	var v []string
	if p.CPUs != nil && *p.CPUs > runtime.NumCPU() {
		v = append(v, fmt.Sprintf("request cpus = %d — this Mac has %d", *p.CPUs, runtime.NumCPU()))
	}
	if p.Memory != nil {
		if want, err := parseSize(*p.Memory); err == nil {
			if host := hostMemoryBytes(); host > 0 && want > host/2 {
				v = append(v, fmt.Sprintf("request memory = %s — more than half of this Mac's %d GiB", *p.Memory, host>>30))
			}
		}
	}
	return v
}

var sizeParts = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)(GiB|MiB|GB|MB|G|M)$`)

// ParseSize converts "8GiB" / "512MiB" style strings to bytes.
func ParseSize(s string) (uint64, error) { return parseSize(s) }

// parseSize converts "8GiB" / "512MiB" style strings to bytes. G/GB are
// treated as GiB, matching how Lima reads them.
func parseSize(s string) (uint64, error) {
	m := sizeParts.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("size %q", s)
	}
	f, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, err
	}
	switch m[2] {
	case "GiB", "G", "GB":
		return uint64(f * (1 << 30)), nil
	default:
		return uint64(f * (1 << 20)), nil
	}
}

// refuseSymlink rejects a project config file that is a symlink: the file is
// read from the repository, and a link could point at any host file.
func refuseSymlink(path string) error {
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return nil //nolint:nilerr // absent or unreadable is readFile's job to report
	}
	return fmt.Errorf("%s is a symlink; the project config must be a regular file in the repository", path)
}
