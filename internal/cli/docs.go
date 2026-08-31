package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/corral-sh/corral/internal/config"
	"github.com/corral-sh/corral/internal/policy"
)

// The feature catalog. A new user — or the AI assistant they point at
// the tool — should be able to enumerate everything Corral does without
// reading the README end to end, and on a machine that has only the binary.
// The catalog is *derived* from the code (command tree, config schema, trust
// table, defaults, known toolchains) plus the one-line descriptions below, so
// it cannot drift from what ships; docs/FEATURES.md is its checked-in
// rendering and a test fails when the two differ (`make docs` regenerates).

// keyDocs describes every config key. TestEveryConfigKeyDocumented enforces
// coverage when a key is added to config.File.
var keyDocs = map[string]string{ //nolint:gosec // G101: documentation strings mention "token"/"credential"; no secret here
	"default_agent":        "Agent started by the bare dashboard `enter` and used for login hints.",
	"cpus":                 "vCPUs per box; a project may set it, capped against the host.",
	"memory":               "RAM ceiling per box (e.g. `4GiB`); the guest never returns memory to the Mac, so this is the eventual cost. Capped against the host.",
	"disk":                 "Sparse VM disk size; only used blocks cost anything.",
	"yolo":                 "Skip the agent's own permission prompts inside the box (the VM is the boundary). `--ask` keeps them for one run.",
	"stop_on_exit":         "Stop the box when the session ends.",
	"readonly_project":     "Mount the project read-only in the box.",
	"shared_agent_state":   "Deprecated alias of `agent_state`: true = shared, false = isolated.",
	"agent_state":          "Where the agent login lives: `shared` (host dir mounted rw), `seeded` (copied in once), `isolated` (log in per box).",
	"git_identity":         "Forward git user.name / user.email (never keys) into the box.",
	"ssh_agent":            "Forward the SSH agent socket into the box.",
	"no_env_passthrough":   "Disable automatic forwarding of the agent's credential variables (`forward_env`).",
	"protect_git_metadata": "Shadow `.git/config` and `.git/hooks` in the box so in-box edits cannot reach what the Mac executes.",
	"name":                 "Override the box name (default: `<dir-slug>-<hash>`).",
	"network":              "`full` (internet), `broker` (only `egress` hosts through the allow-list proxy on the Mac, DNS closed, sudo removed), `offline` (nothing but the Mac, sudo removed).",
	"rosetta":              "Run amd64 binaries and `--platform linux/amd64` containers inside the arm64 box (Apple Silicon).",
	"idle_stop":            "Stop a box with no live session after this long (`30m`, `off`).",
	"golden":               "Build the box by cloning the shared golden image (toolchains + agents) instead of from scratch.",
	"source":               "`mount` (checkout mounted live at its real path) or `clone` (nothing mounted; the box clones the repo at session start).",
	"snapshot":             "`auto` takes an APFS clone of the box disk at each session start from stopped; `corral undo` restores it.",
	"snapshots_keep":       "How many auto snapshots to keep.",
	"profile":              "Named security floor: `default`, `offline`, `strict` (broker + isolated login + no ssh agent + no passthrough + git shadow). Keys may tighten beyond it, never loosen.",
	"forward_env":          "Host variables forwarded by name when set (over SSH SendEnv, never argv).",
	"env":                  "`KEY=value` literals set in the box; a bare `KEY` forwards the host value (trusted layers only).",
	"env_from_host":        "`GUEST_VAR=HOST_VAR` aliases; the session refuses to start if the host variable is unset.",
	"keychain_env":         "Variables whose value is read from the macOS Keychain (service = name) at launch when not exported; a missing item refuses to start.",
	"max_running":          "Admission: refuse to start another box when this many are running (exit 75, requeue); 0 = no limit.",
	"memory_reserve":       "Admission: RAM that must stay free for macOS — a box is refused (exit 75) when the running boxes' measured footprint plus its `memory` would exceed host RAM minus this.",
	"timeout":              "Default for `run --timeout`: end the session after this long (SIGTERM, then SIGKILL after 10 s), exit 124.",
	"env_file":             "`KEY=value` file consulted after the exported environment and before the Keychain — the credential path for an unattended host (launchd). Must be 0600, yours, under your home; refused otherwise.",
	"packages":             "Extra Ubuntu apt packages installed in the box.",
	"toolchain_versions":   "`{ flutter = \"3.44.2\" }` or `\"3.44.2@<commit>\"`: pin a toolchain's release (flutter today); part of the golden image's identity, verified against the commit when given.",
	"toolchains":           "Preinstalled toolchains (see Toolchains below); they define the golden image. Unioned across layers; an explicit `[]` drops the default node.",
	"mounts":               "Extra host directories mounted into the box (`host:guest[:ro]`); home and credential directories are refused.",
	"provision":            "Repository scripts run at the end of provisioning as the box user (`# corral: system` for root in full mode); a failure fails the start.",
	"hide":                 "Project paths shown empty inside the box (`.env`, `secrets/`).",
	"box_dirs":             "Project directories kept on the box disk and bind-mounted over the mount (`node_modules`, `build`): fast installs, empty on the Mac.",
	"api_brokers":          "`[[api_brokers]] name, upstream, token, auth (header|bearer|basic), header/user, allow = [\"METHOD /path/**\"]`: a credential-holding proxy on the Mac — the box calls `$CORRAL_API_<NAME>/…`, the Mac adds the token and forwards only allow-listed method+path calls; every call audited.",
	"egress":               "Allow-list for `network = \"broker\"`: exact hosts or `*.suffix`, `:port` to widen beyond 80/443; git_tokens hosts are added automatically.",
	"git_tokens":           "`{ \"<host>\" = \"<HOST_VAR>\" }` or `{ \"<host>\" = { token = \"<HOST_VAR>\", user = \"gitlab+deploy-token-<id>\" } }`: HTTPS git credential for the box, offered only to that host; the variable may come from `keychain_env`.",
}

type catalogKey struct {
	Key         string `json:"key"`
	Type        string `json:"type"`
	Default     string `json:"default,omitempty"`
	Trust       string `json:"trust"`
	Description string `json:"description"`
}

type catalogCommand struct {
	Command string `json:"command"`
	Group   string `json:"group,omitempty"`
	Summary string `json:"summary"`
}

type catalogItem struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type catalog struct {
	Version   string           `json:"version"`
	Commands  []catalogCommand `json:"commands"`
	Config    []catalogKey     `json:"config"`
	Modes     []catalogItem    `json:"modes"`
	Controls  []catalogItem    `json:"guest_controls"`
	Toolchain []catalogItem    `json:"toolchains"`
	GuestEnv  []catalogItem    `json:"guest_env"`
	Layout    []catalogItem    `json:"host_layout"`
	ExitCodes []catalogItem    `json:"exit_codes"`
}

// exitCodeDocs is the outcome vocabulary of `corral run`: the agent's
// own status passes through unchanged; Corral's own outcomes use sysexits(3)
// values agents do not, so a queue can tell "task failed" from "requeue".
var exitCodeDocs = []catalogItem{
	{"0–255 (agent's)", "The command/agent finished; this is its own exit status (`outcome = \"exit\"`)."},
	{"78", "`EX_CONFIG` — `--preflight` refused the session: a declared grant does not work from inside the box. Fix the configuration; do not retry (`outcome = \"preflight-refused\"`)."},
	{"75", "`EX_TEMPFAIL` — admission refused: `max_running` reached or the memory budget (`memory_reserve`) would be exceeded. Requeue (`outcome = \"admission-refused\"`)."},
	{"69", "`EX_UNAVAILABLE` — the box became unreachable during the session (SSH lost, instance gone, guest OOM-kill when detectable). Requeue and alert (`outcome = \"unreachable\"`)."},
	{"124", "`--timeout` elapsed; the session was terminated (`outcome = \"timeout\"`)."},
	{"1", "Any other Corral error (configuration refused by policy, Lima failure); the message names the next command."},
	{"run --result <file>", "Writes one JSON object per run — `box, agent, outcome, exit_code, reason, started, ended, duration, forwarded_env` — the same record the audit log gets."},
}

var modeDocs = []catalogItem{
	{"network = full | broker | offline", "Internet · allow-list proxy on the Mac (DNS closed, sudo removed) · nothing but the Mac (sudo removed). A repository may only tighten."},
	{"source = mount | clone", "Checkout mounted live at its real path · nothing mounted, the box clones the repository at session start with `git_tokens`."},
	{"agent_state = shared | seeded | isolated", "One login for all boxes (host dir mounted) · copied in once · per box. Tighten-only."},
	{"profile = default | offline | strict", "Named floors; `strict` = broker + isolated + no ssh agent + no env passthrough + git shadow."},
	{"snapshot = off | auto", "APFS clone of the box disk at each session start; `corral undo` rolls back."},
	{"golden = true | false", "Clone the shared golden image (15 s) or build from scratch (2–4 min)."},
	{"api_brokers", "Credential-holding API proxy on the Mac for GitLab/Jira-style REST calls: scoped by method+path, token never in the box, works in `full` and `broker` network modes (not `offline`)."},
}

var controlDocs = []catalogItem{
	{"corral-broker", "nftables funnel: only the broker port on the Mac gateway; DNS closed; sudo removed. Re-applied before every session; a session refuses to start if it is not active."},
	{"corral-offline", "nftables reject-all except the Mac; sudo removed. Same session gate."},
	{"corral-git-shadow", "Guest-local `.git/config`, empty `.git/hooks` over the mount (`protect_git_metadata`)."},
	{"corral-hide", "Empty box-owned file/tmpfs over each `hide` path."},
	{"corral-boxdirs", "Box-disk directory bind-mounted over each `box_dirs` path."},
	{"provision failure record", "A repository provision script that exits non-zero is recorded in `/corral/runtime/provision/`; corral refuses to start after create/start."},
	{"egress broker (host)", "Per-box CONNECT/forward proxy on `127.0.0.1:<port>`, allow-list decided on the Mac, denials audited by name (`corral egress`)."},
	{"audit log", "`~/.corral/logs/sessions.jsonl`: launches, variable *names*, denials, snapshots, deletes (`corral audit`)."},
}

var toolchainDocs = map[string]string{
	"node":    "Node.js 22 LTS from nodejs.org (SHA-256 verified), npm `min-release-age = 7`.",
	"go":      "Latest stable Go from go.dev (SHA-256 verified).",
	"python":  "Python 3, pip, venv, pipx (Ubuntu apt).",
	"docker":  "Docker Engine + compose + buildx inside the box; the box user is in the docker group.",
	"java":    "OpenJDK 17, JAVA_HOME.",
	"android": "Android SDK cmdline-tools (SHA-256 pinned), platform-tools, android-35, build-tools 35.0.0, JDK; amd64 multiarch so aapt2 runs under Rosetta.",
	"flutter": "Flutter stable (3.47.1 by default; `toolchain_versions` pins another release), release commit pinned and verified, Dart + Android artifacts precached.",
}

var guestEnvDocs = []catalogItem{
	{"CORRAL=1", "Set in every shell inside a box (also `/etc/corral-release`)."},
	{"CORRAL_NAME / CORRAL_PROJECT / CORRAL_VERSION / CORRAL_AGENT", "Session identity variables."},
	{"CORRAL_NETWORK / CORRAL_SOURCE", "The box's network and source mode."},
	{"CORRAL_YOLO", "1 when the agent wrapper adds its skip-prompts flags; 0 with `--ask`."},
	{"CORRAL_USER / CORRAL_HOME", "In provision scripts: the box user (carries the Mac uid) and their home."},
	{"HTTP_PROXY / HTTPS_PROXY / NO_PROXY", "In broker mode: the allow-list proxy on the Mac gateway."},
	{"CORRAL_API_<NAME>", "Base URL of an `api_brokers` route (`http://192.168.5.2:<port>/<name>`); the credential is added on the Mac, never present in the box."},
	{"CLAUDE_CONFIG_DIR", "Claude Code state relocated to `/corral/agents/claude`; `DISABLE_AUTOUPDATER=1`."},
}

var layoutDocs = []catalogItem{
	{"~/.corral/config.toml", "Global config (`corral setup`)."},
	{"<project>/.corral.toml", "Repository config — can only shape the guest (see trust column)."},
	{"~/.corral/projects/<box>.toml", "Your per-project trusted layer (egress, tokens, mounts)."},
	{"~/.corral/lima/<box>/", "Lima instance: disks, per-box SSH key, ssh.config (LIMA_HOME)."},
	{"~/.corral/boxes/<box>.json", "Metadata, rendered template and its hash (drift detection)."},
	{"~/.corral/agents/<agent>/", "Shared agent login/state."},
	{"~/.corral/snapshots/<box>/", "APFS-clone snapshots."},
	{"~/.corral/ssh/config", "One `Include` per box for `corral code` / `ssh lima-<box>`."},
}

func buildCatalog(root *cobra.Command) catalog {
	c := catalog{Version: Version}
	groups := map[string]string{}
	for _, g := range root.Groups() {
		groups[g.ID] = strings.TrimSuffix(g.Title, ":")
	}
	var walk func(prefix string, cmd *cobra.Command)
	walk = func(prefix string, cmd *cobra.Command) {
		for _, sub := range cmd.Commands() {
			if sub.Hidden || sub.Name() == "help" || sub.Name() == "completion" {
				continue
			}
			use := prefix + sub.Use
			c.Commands = append(c.Commands, catalogCommand{Command: use, Group: groups[sub.GroupID], Summary: sub.Short})
			walk(prefix+sub.Name()+" ", sub)
		}
	}
	walk("corral ", root)

	defaults := reflect.ValueOf(config.Defaults())
	ft := reflect.TypeOf(config.File{})
	for i := 0; i < ft.NumField(); i++ {
		f := ft.Field(i)
		key, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
		if key == "" {
			continue
		}
		trust, _ := policy.TrustOf(key)
		typ := f.Type
		if typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		def := ""
		v := defaults.Field(i)
		if v.Kind() == reflect.Pointer && !v.IsNil() {
			def = fmt.Sprint(v.Elem().Interface())
		} else if v.Kind() == reflect.Slice && v.Len() > 0 {
			def = fmt.Sprint(v.Interface())
		}
		c.Config = append(c.Config, catalogKey{Key: key, Type: typ.String(), Default: def, Trust: trust.String(), Description: keyDocs[key]})
	}
	c.Modes = modeDocs
	c.Controls = controlDocs
	for _, t := range config.KnownToolchains {
		c.Toolchain = append(c.Toolchain, catalogItem{t, toolchainDocs[t]})
	}
	sort.SliceStable(c.Toolchain, func(a, b int) bool { return c.Toolchain[a].Name < c.Toolchain[b].Name })
	c.GuestEnv = guestEnvDocs
	c.Layout = layoutDocs
	c.ExitCodes = exitCodeDocs
	return c
}

func renderCatalogMarkdown(w io.Writer, c catalog) {
	fmt.Fprintln(w, "# Corral — feature catalog")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "One line per command, config key, mode and guest control. Generated from the code by `corral docs`")
	fmt.Fprintln(w, "(`make docs` rewrites this file; a test fails when it is stale). `corral docs --json` prints the same")
	fmt.Fprintln(w, "catalog as JSON. Narrative and examples: [README](../README.md) · threat model: [SECURITY.md](SECURITY.md) ·")
	fmt.Fprintln(w, "design: [ARCHITECTURE.md](ARCHITECTURE.md) · measurements: [FEASIBILITY.md](FEASIBILITY.md).")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "**What it is:** each project gets its own Linux VM (Lima + Apple Virtualization) in which an AI coding agent")
	fmt.Fprintln(w, "runs with no permission prompts; the project is mounted at its real path, the Mac's home, keys and other")
	fmt.Fprintln(w, "repositories are not there. A repository's own config can shape the guest but never widen what the box reaches.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "## Commands")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| Command | Group | Does |")
	fmt.Fprintln(w, "|---|---|---|")
	for _, cmd := range c.Commands {
		fmt.Fprintf(w, "| `%s` | %s | %s |\n", cmd.Command, cmd.Group, cell(cmd.Summary))
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Every command takes `-C <dir>` (project) and `--box <name>`. Layers: defaults < `~/.corral/config.toml` <")
	fmt.Fprintln(w, "`<project>/.corral.toml` (restricted) < `~/.corral/projects/<box>.toml` (trusted) < flags.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "## Configuration keys")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Trust: **project-ok** — a repository may set it (guest-only effect) · **project-may-tighten** — a repository may only")
	fmt.Fprintln(w, "make it stricter · **trusted-only** — refused in the repository file (it would widen what the box reaches on the Mac).")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| Key | Type | Default | Trust | Meaning |")
	fmt.Fprintln(w, "|---|---|---|---|---|")
	for _, k := range c.Config {
		def := k.Default
		if def != "" {
			def = "`" + def + "`"
		}
		fmt.Fprintf(w, "| `%s` | %s | %s | %s | %s |\n", k.Key, k.Type, def, k.Trust, cell(k.Description))
	}
	section := func(title, intro string, items []catalogItem) {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "## "+title)
		fmt.Fprintln(w)
		if intro != "" {
			fmt.Fprintln(w, intro)
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w, "| | |")
		fmt.Fprintln(w, "|---|---|")
		for _, it := range items {
			fmt.Fprintf(w, "| `%s` | %s |\n", it.Name, cell(it.Description))
		}
	}
	section("Modes", "", c.Modes)
	section("Guest controls", "What enforces the configuration inside the box (systemd units, re-applied before every session) and on the Mac.", c.Controls)
	section("Toolchains", "`toolchains = [...]`; every download is pinned and checksum-verified, no `curl | bash` except the agent vendor's own installer.", c.Toolchain)
	section("Environment inside the box", "", c.GuestEnv)
	section("Host layout", "", c.Layout)
	section("Exit codes and outcomes", "`corral run` for queues and scripts: the agent's status passes through; Corral's own outcomes use sysexits(3) codes agents do not.", c.ExitCodes)
}

func cell(s string) string { return strings.ReplaceAll(s, "|", "\\|") }

func newDocsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "docs",
		Short: "Print the feature catalog (every command, config key, mode, control) — for humans and AI assistants",
		Long: `Prints docs/FEATURES.md as shipped in this binary: every command, configuration key (with type,
default and trust class), mode, guest control, toolchain and environment variable, one line each.
Point an AI assistant at ` + "`corral docs`" + ` (or ` + "`--json`" + `) to let it discover the tool without the repository.`,
		GroupID: "insight",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := buildCatalog(cmd.Root())
			if asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(c)
			}
			renderCatalogMarkdown(os.Stdout, c)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the catalog as JSON")
	return cmd
}
