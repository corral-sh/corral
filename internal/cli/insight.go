package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/corral-sh/corral/internal/agent"
	"github.com/corral-sh/corral/internal/box"
	"github.com/corral-sh/corral/internal/config"
	"github.com/corral-sh/corral/internal/lima"
	"github.com/corral-sh/corral/internal/paths"
	"github.com/corral-sh/corral/internal/policy"
	"github.com/corral-sh/corral/internal/ui"
)

func agentNames() []string { return agent.Names() }

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// ---------------------------------------------------------------------------
// doctor
// ---------------------------------------------------------------------------

type check struct {
	name   string
	ok     bool
	detail string
	fix    string
}

// hostChecks runs the prerequisite checks. fatalOnly restricts to the ones
// that must pass before a box can be created.
func hostChecks(ctx context.Context) []check {
	var out []check

	// OS
	if runtime.GOOS != "darwin" {
		out = append(out, check{"macOS", false, runtime.GOOS, "Corral v0.x supports macOS only (Windows/Linux are on the roadmap)"})
	} else {
		ver := macOSVersion()
		ok := macOSAtLeast(ver, 13, 5)
		out = append(out, check{"macOS ≥ 13.5", ok, ver, "Apple Virtualization (vz) with virtiofs needs macOS 13.5 or newer"})
	}
	out = append(out, check{"CPU architecture", true, runtime.GOARCH, ""})
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		// Optional: only boxes with rosetta = true need it. Lima installs it on
		// demand but the prompt is easy to miss, so surface it here.
		err := exec.CommandContext(ctx, "arch", "-x86_64", "/usr/bin/true").Run()
		detail := "installed"
		if err != nil {
			detail = "not installed (only needed for rosetta = true)"
		}
		out = append(out, check{"Rosetta 2 (optional)", err == nil, detail, "softwareupdate --install-rosetta --agree-to-license"})
	}

	// Lima
	lh, _ := paths.LimaHome()
	lc, err := lima.New(lh)
	if err != nil {
		out = append(out, check{"Lima installed", false, "limactl not found", "brew install lima"})
	} else {
		v, verr := lc.Version(ctx)
		ok := verr == nil && lima.VersionAtLeast(v, lima.MinVersion)
		out = append(out, check{"Lima ≥ " + lima.MinVersion, ok, v, "brew install " + lima.PinnedFormula})
		if verr == nil {
			// Advisory (name must not start with "Lima": those block a launch).
			detail := v + " (" + ui.ShortenHome(lc.Bin) + ")"
			if !lima.Tested(v) {
				detail = v + " — Corral is tested with " + lima.TestedVersion + "; the guest OS image is pinned per Lima release"
			}
			out = append(out, check{"Tested Lima release", lima.Tested(v), detail, "brew install " + lima.PinnedFormula})
		}
	}

	// ssh
	if _, err := exec.LookPath("ssh"); err != nil {
		out = append(out, check{"ssh client", false, "not found", "install Xcode command line tools"})
	} else {
		out = append(out, check{"ssh client", true, "found", ""})
	}
	// Editors (optional): only `corral code` needs them.
	if _, err := exec.LookPath("code"); err != nil {
		out = append(out, check{"VS Code CLI (optional)", true, "not found (only needed for corral code)", ""})
	} else {
		out = append(out, check{"VS Code CLI (optional)", true, "found", ""})
	}

	// Corral home & socket length
	h, err := paths.Home()
	if err != nil {
		out = append(out, check{"Corral home", false, err.Error(), ""})
	} else {
		n, err := paths.MaxBoxNameLen()
		ok := err == nil && n >= 8
		detail := ui.ShortenHome(h)
		if err != nil {
			detail = err.Error()
		}
		out = append(out, check{"Corral home", ok, detail, "export CORRAL_HOME to a shorter path"})
	}

	// Box budget: vz has no balloon device, so a running box grows into its
	// memory ceiling and never gives it back until stopped.
	if host := policy.HostMemoryBytes(); host > 0 {
		d, _ := config.Resolve(config.Defaults())
		per, _ := parseSizeGiB(d.Memory)
		hostGiB := float64(host) / (1 << 30)
		n := int((hostGiB - 8) / per) // leave ~8 GiB for macOS and the editor
		if n < 1 {
			n = 1
		}
		out = append(out, check{"Box budget", hostGiB >= 16, fmt.Sprintf("%.0f GiB RAM → about %d concurrent box(es) at the default %s, keeping 8 GiB for macOS", hostGiB, n, d.Memory), "stop idle boxes (idle_stop does this) or lower `memory`"})
	}

	// Disk space: an unattended host fills its disk with nobody
	// watching. Warn below 20 GiB or 10 % free and name what would free it.
	if free, total := diskGiB(h); free >= 0 {
		ok := free >= 20 && (total <= 0 || free/total >= 0.10)
		detail := fmt.Sprintf("%.0f GiB free", free)
		if total > 0 {
			detail = fmt.Sprintf("%.0f of %.0f GiB free (%.0f %%)", free, total, 100*free/total)
		}
		fix := "each box needs ~3 GiB initially and grows as tools are installed"
		if !ok {
			fix = diskReclaimHint()
		}
		out = append(out, check{"Free disk ≥ 20 GiB and ≥ 10 %", ok, detail, fix})
	}

	// Yolobox coexistence is fine; Docker not required.
	if _, err := exec.LookPath("docker"); err == nil {
		out = append(out, check{"Docker (not required)", true, "present — not used by Corral; the box runs its own engine if toolchains includes docker", ""})
	}
	return out
}

func newDoctorCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "doctor [box]",
		Short: "Check the host; with a box name, preflight what the project declared from inside the box",
		Long: `Without arguments: host prerequisites (macOS, Lima, Rosetta, memory budget) and Corral health.

With a box name (or -C <project>): boots the box if needed and checks, from inside it, what this
project's configuration declared — agent and toolchains installed, in-guest controls active, each
git_tokens host and egress entry reachable through the box's own network mode, granted variables
set on the Mac, provision scripts of this boot. Exit status is non-zero when anything fails.`,
		GroupID: "insight",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 || gf.project != "" || gf.name != "" {
				var b *box.Box
				var err error
				if len(args) == 1 {
					b, err = openBoxByName(args[0])
				} else {
					b, err = openBox()
				}
				if err != nil {
					return err
				}
				return runBoxDoctor(cmd.Context(), b, asJSON)
			}
			ui.Banner(os.Stdout, Version)
			fmt.Println()
			checks := hostChecks(cmd.Context())
			bad := 0
			for _, c := range checks {
				mark := ui.Ok.Render("✓")
				if !c.ok {
					mark = ui.Bad.Render("✗")
					bad++
				}
				fmt.Printf("  %s %-24s %s\n", mark, c.name, ui.Subtle.Render(c.detail))
				if !c.ok && c.fix != "" {
					fmt.Printf("      %s %s\n", ui.Warn.Render("fix:"), c.fix)
				}
			}
			fmt.Println()
			rows, err := collectRows(cmd.Context())
			if err == nil {
				running := 0
				var disk int64
				for _, r := range rows {
					if r.Status == "Running" {
						running++
					}
					disk += r.DiskUsed
				}
				fmt.Printf("  %s %d boxes, %d running, %s on disk\n", ui.Info.Render("ℹ"), len(rows), running, ui.HumanBytes(disk))
			}
			for _, a := range agent.All() {
				dir, _ := paths.AgentStateDir(a.Name())
				loggedIn := agentLoggedIn(a.Name(), dir)
				state := ui.Subtle.Render("no login stored yet — " + a.LoginHint())
				if loggedIn {
					state = ui.Ok.Render("login stored in " + ui.ShortenHome(dir))
				}
				fmt.Printf("  %s %s: %s\n", ui.Info.Render("ℹ"), a.Name(), state)
			}
			if bad > 0 {
				return fmt.Errorf("%d check(s) failed", bad)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output (box preflight)")
	return cmd
}

// agentLoggedIn is a heuristic: the state dir has a credentials file.
func agentLoggedIn(name, dir string) bool {
	switch name {
	case "claude":
		if _, err := os.Stat(filepath.Join(dir, ".credentials.json")); err == nil {
			return true
		}
		if os.Getenv("ANTHROPIC_API_KEY") != "" || os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") != "" {
			return true
		}
	}
	return false
}

// checkHost is the fast pre-flight before touching Lima.
func checkHost(ctx context.Context, b *box.Box) error {
	for _, c := range hostChecks(ctx) {
		if !c.ok && (strings.HasPrefix(c.name, "macOS") || strings.HasPrefix(c.name, "Lima") || c.name == "Corral home") {
			if strings.HasPrefix(c.name, "Lima installed") && ui.IsTTY() {
				if ui.Confirm(os.Stderr, "Lima is not installed. Install it now with Homebrew?", true) {
					if err := brewInstallLima(ctx); err != nil {
						return err
					}
					lh, _ := paths.LimaHome()
					lc, err := lima.New(lh)
					if err != nil {
						return err
					}
					b.Lima = lc
					continue
				}
			}
			return fmt.Errorf("%s: %s — %s", c.name, c.detail, c.fix)
		}
	}
	return nil
}

func brewInstallLima(ctx context.Context) error {
	brew, err := exec.LookPath("brew")
	if err != nil {
		return fmt.Errorf("homebrew not found; install Lima manually: https://lima-vm.io/docs/installation/")
	}
	return ui.RunWithProgress(ctx, "brew install "+lima.PinnedFormula, func(report func(string)) error {
		c := exec.CommandContext(ctx, brew, "install", lima.PinnedFormula)
		out, err := c.CombinedOutput()
		for _, l := range strings.Split(string(out), "\n") {
			if strings.TrimSpace(l) != "" {
				report(l)
			}
		}
		return err
	})
}

func macOSVersion() string {
	out, err := exec.CommandContext(context.Background(), "sw_vers", "-productVersion").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func macOSAtLeast(ver string, major, minor int) bool {
	parts := strings.Split(ver, ".")
	if len(parts) < 1 {
		return false
	}
	maj, _ := strconv.Atoi(parts[0])
	min := 0
	if len(parts) > 1 {
		min, _ = strconv.Atoi(parts[1])
	}
	return maj > major || (maj == major && min >= minor)
}

// ---------------------------------------------------------------------------
// agents
// ---------------------------------------------------------------------------

func newAgentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "agents",
		Short:   "List supported agents and manage their persistent state",
		GroupID: "insight",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var rows [][]string
			for _, a := range agent.All() {
				dir, _ := paths.AgentStateDir(a.Name())
				login := ui.Subtle.Render("not yet")
				if agentLoggedIn(a.Name(), dir) {
					login = ui.Ok.Render("stored")
				}
				rows = append(rows, []string{a.Name(), a.Summary(), strings.Join(a.YoloArgs(), " "), login, ui.ShortenHome(dir)})
			}
			fmt.Println(ui.Table([]string{"AGENT", "DESCRIPTION", "YOLO FLAGS", "LOGIN", "STATE DIR"}, rows))
			return nil
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "import <agent>",
		Short: "Copy host settings/skills (never credentials) into the agent's box state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, ok := agent.Lookup(args[0])
			if !ok {
				return fmt.Errorf("unknown agent %q (known: %s)", args[0], strings.Join(agent.Names(), ", "))
			}
			return importAgentConfig(a, false)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "logout <agent>",
		Short: "Remove the stored login for an agent from ~/.corral/agents",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, ok := agent.Lookup(args[0])
			if !ok {
				return fmt.Errorf("unknown agent %q", args[0])
			}
			dir, err := paths.AgentStateDir(a.Name())
			if err != nil {
				return err
			}
			removed := 0
			for _, f := range []string{".credentials.json"} {
				if err := os.Remove(filepath.Join(dir, f)); err == nil {
					removed++
				}
			}
			ui.Success(os.Stdout, "removed %d credential file(s) from %s", removed, ui.ShortenHome(dir))
			return nil
		},
	})
	return cmd
}

// importAgentConfig copies the agent's HostConfigImports into its state dir.
func importAgentConfig(a agent.Agent, quiet bool) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dst, err := paths.AgentStateDir(a.Name())
	if err != nil {
		return err
	}
	n := 0
	for src, rel := range a.HostConfigImports() {
		from := filepath.Join(home, src) //nolint:gosec // src comes from the compiled-in agent registry, not user input
		info, err := os.Stat(from)
		if err != nil {
			continue
		}
		to := filepath.Join(dst, rel)
		if info.IsDir() {
			if err := copyDir(from, to); err != nil {
				return fmt.Errorf("copy %s: %w", from, err)
			}
		} else {
			if err := copyFile(from, to); err != nil {
				return fmt.Errorf("copy %s: %w", from, err)
			}
		}
		n++
		if !quiet {
			ui.Success(os.Stdout, "%s → %s", ui.ShortenHome(from), ui.ShortenHome(to))
		}
	}
	if n == 0 {
		ui.Warning(os.Stderr, "nothing to import for %s", a.Name())
	} else if quiet {
		ui.Success(os.Stderr, "imported %d item(s) of %s config into %s", n, a.Name(), ui.ShortenHome(dst))
	}
	return nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600) //nolint:gosec // dst is under ~/.corral/agents, built from registry constants
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			resolved, err := filepath.EvalSymlinks(p)
			if err != nil {
				return nil //nolint:nilerr // dangling symlink: skip it, keep importing the rest
			}
			return copyFile(resolved, target)
		}
		return copyFile(p, target)
	})
}

// ---------------------------------------------------------------------------
// audit
// ---------------------------------------------------------------------------

func newAuditCmd() *cobra.Command {
	var n int
	var jsonOut bool
	cmd := &cobra.Command{
		Use:     "audit",
		Short:   "Show the session audit log (who launched what, where, with which env)",
		GroupID: "insight",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			events, err := box.ReadAudit(n)
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(events)
			}
			if len(events) == 0 {
				fmt.Println(ui.Subtle.Render("no sessions recorded yet"))
				return nil
			}
			var rows [][]string
			for _, e := range events {
				detail := ""
				switch e.Event {
				case "launch":
					detail = strings.Join(e.Argv, " ")
					if len(e.Forwarded) > 0 {
						detail += ui.Subtle.Render("  env: " + strings.Join(e.Forwarded, ","))
					}
				case "exit":
					if e.ExitCode != nil {
						detail = fmt.Sprintf("exit %d after %s", *e.ExitCode, e.Duration)
					}
				case "delete", "idle-stop", "stop", "create":
					if e.Cmd != "" {
						detail = "by `corral " + e.Cmd + "`"
						if e.Parent != "" {
							detail += ui.Subtle.Render("  from " + e.Parent)
						}
					}
				}
				rows = append(rows, []string{e.Time.Format("01-02 15:04:05"), e.Event, e.Box, e.Agent, ui.Truncate(detail, 70)})
			}
			fmt.Println(ui.Table([]string{"TIME", "EVENT", "BOX", "AGENT", "DETAIL"}, rows))
			logs, _ := paths.LogsDir()
			fmt.Println(ui.Subtle.Render("  full log: " + ui.ShortenHome(filepath.Join(logs, "sessions.jsonl"))))
			return nil
		},
	}
	cmd.Flags().IntVarP(&n, "lines", "n", 30, "number of events to show")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")
	return cmd
}

// ---------------------------------------------------------------------------
// config
// ---------------------------------------------------------------------------

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "config",
		Short:   "Show the resolved configuration for the current project",
		GroupID: "insight",
		Long: `Shows the effective configuration: built-in defaults, overlaid with
~/.corral/config.toml, overlaid with .corral.toml from the project,
overlaid with your host-side ~/.corral/projects/<box>.toml.

The project file belongs to the repository, so it is not trusted with keys that
widen host access — those are refused and the box does not start:
  trusted-only (global config):  ` + strings.Join(policy.TrustedOnlyKeys(), ", ") + `
  project may only tighten:      ` + strings.Join(policy.TightenOnlyKeys(), ", ") + `

  corral config init        write a commented .corral.toml into the project
  corral config init --host write ~/.corral/projects/<box>.toml (per-project privilege, yours)
  corral setup            interactive global configuration`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			project, err := projectDir()
			if err != nil {
				return err
			}
			cfg, err := loadConfig(project, gf.name)
			if err != nil {
				return err
			}
			gp, _ := paths.GlobalConfigFile()
			fmt.Println(ui.Header.Render("Sources"))
			ui.KV(os.Stdout, "defaults", "built-in")
			ui.KV(os.Stdout, "global", ui.ShortenHome(gp)+existsTag(gp))
			pp := filepath.Join(project, config.ProjectFileName)
			ui.KV(os.Stdout, "project", ui.ShortenHome(pp)+existsTag(pp)+ui.Subtle.Render("  (repo-owned: guest-only keys)"))
			if hp, err := hostProjectConfig(project, cfg.Name); err == nil {
				ui.KV(os.Stdout, "host-project", ui.ShortenHome(hp)+existsTag(hp)+ui.Subtle.Render("  (yours: per-project privilege)"))
			}
			fmt.Println()
			fmt.Println(ui.Header.Render("Resolved"))
			printResolved(os.Stdout, cfg)
			return nil
		},
	}
	var host bool
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Write a commented .corral.toml into the project (--host: your per-project file instead)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			project, err := projectDir()
			if err != nil {
				return err
			}
			if host {
				gp, _ := paths.GlobalConfigFile()
				hp, err := hostProjectConfig(project, effectiveBoxName(project, gf.name, gp))
				if err != nil {
					return err
				}
				if _, err := os.Stat(hp); err == nil {
					return fmt.Errorf("%s already exists", hp)
				}
				if err := os.MkdirAll(filepath.Dir(hp), 0o700); err != nil {
					return err
				}
				if err := os.WriteFile(hp, []byte(fmt.Sprintf(hostProjectTemplate, project)), 0o600); err != nil {
					return err
				}
				ui.Success(os.Stdout, "wrote %s", ui.ShortenHome(hp))
				return nil
			}
			p := filepath.Join(project, config.ProjectFileName)
			if _, err := os.Stat(p); err == nil {
				return fmt.Errorf("%s already exists", p)
			}
			if err := os.WriteFile(p, []byte(projectTemplate), 0o644); err != nil { //nolint:gosec // a config file meant to be committed; world-readable is correct
				return err
			}
			ui.Success(os.Stdout, "wrote %s", ui.ShortenHome(p))
			return nil
		},
	}
	initCmd.Flags().BoolVar(&host, "host", false, "write your host-side per-project file (~/.corral/projects/<box>.toml) instead of the repo file")
	cmd.AddCommand(initCmd)
	cmd.AddCommand(&cobra.Command{
		Use:   "path",
		Short: "Print the global config file path",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			gp, err := paths.GlobalConfigFile()
			if err != nil {
				return err
			}
			fmt.Println(gp)
			return nil
		},
	})
	return cmd
}

func existsTag(p string) string {
	if _, err := os.Stat(p); err == nil {
		return ui.Ok.Render("  (present)")
	}
	return ui.Subtle.Render("  (absent)")
}

const projectTemplate = `# Corral project configuration. Commit this file so the whole team gets
# the same box. Every key is optional; CLI flags > this file > ~/.corral/config.toml.
#
# This file is part of the repository, so it can only shape the guest — it can
# never widen what the box reaches on your Mac. Keys that grant host access
# (ssh_agent, mounts, git_tokens, forward_env, env_from_host, name, and bare
# "KEY" entries in env) are refused here; put them in ~/.corral/config.toml.
# yolo / readonly_project / agent_state / no_env_passthrough /
# protect_git_metadata / network / source / profile may only be made stricter here.

# A profile is a floor: keys may tighten beyond it, never loosen below it,
# whichever file or flag set them. "offline" = no egress, sudo removed.
# "strict" = offline + private agent login + no SSH agent + no ambient env +
# .git metadata shadowed. "corral info" prints the effective profile.
# profile = "strict"

# Resources for this project's box (applied on create / rebuild).
# cpus = 4
# memory = "4GiB"          # default; use "6GiB" with the docker toolchain or big Go builds
# disk = "60GiB"

# Toolchains installed into the box: node, go, python, docker.
# toolchains = ["node", "go"]      # [] installs none — the default node is dropped
# toolchain_versions = { flutter = "3.44.2" }   # pin a release (or "3.44.2@<commit>"); its own golden image

# Run amd64 binaries and docker --platform linux/amd64 inside the arm64
# box via Rosetta 2 (Apple Silicon only; needs a rebuild).
# rosetta = true

# Extra Ubuntu packages.
# packages = ["default-jdk", "maven"]

# Environment inside the box: literal "KEY=value" only. Forwarding a host
# variable ("KEY", env_from_host, git_tokens, mounts) is global-config only.
# env = ["DEBUG=1", "APP_ENV=dev"]

# Project-local provisioning scripts (run as the box user once at create time;
# add the line "# corral: system" to run as root).
# provision = ["scripts/box-setup.sh"]

# Paths the box must not see (an empty file / directory is shown instead;
# relative to the project, "dir/" for directories). Hygiene, not a boundary.
# hide = [".env", "secrets/"]
# box_dirs = ["node_modules", "build"]   # kept on the box disk: installs at disk speed, empty on the Mac

# No internet from the box (egress rejected, sudo removed so the agent cannot
# undo it). Installs happen at create/rebuild, before the lockdown.
# network = "offline"

# Clone the repository inside the box instead of mounting this checkout: no
# host file is shared; the handoff back is a pushed branch. Needs git_tokens
# for the origin host in your global config.
# source = "clone"

# Mount the project read-only (outputs must go elsewhere).
# readonly_project = true

# Stop the VM when the session ends (saves RAM; next start ~20 s).
# stop_on_exit = false

# Stop the VM after this long without a session ("off" to never). Checked
# whenever corral runs (any command) and by corral gc.
# idle_stop = "30m"
`

const hostProjectTemplate = `# Corral host-side configuration for %s
# This file is yours, not the repository's: it may grant what .corral.toml
# in the repo cannot. Precedence: flags > this file > repo file > global.

# Forward your SSH agent into this box only.
# ssh_agent = true

# Credentials for an unattended host (launchd has no login keychain): KEY=value
# lines, chmod 0600, consulted after the exported environment.
# env_file = "~/.corral/env"

# Extra mounts "host:guest[:ro]". Home, ~/.ssh, ~/.aws etc. are still refused.
# mounts = ["~/Code/utils/lib-go-common:/libs:ro"]

# HTTPS git credentials per host, from a host environment variable.
# git_tokens = { "git.example.com" = "GITLAB_TOKEN" }
# git_tokens = { "git.example.com" = { token = "GITLAB_DEPLOY_TOKEN", user = "gitlab+deploy-token-1" } }  # deploy token

# Forward host variables, by name or under a different name (fails closed if unset).
# forward_env = ["MY_API_KEY"]
# env_from_host = ["GH_TOKEN=CORRAL_READONLY_GH_TOKEN"]

# Loosen what the repo tightened, if you decide to.
# readonly_project = false

# Let the agent write .git/hooks and .git/config on your Mac's copy (they run on
# the host at your next git command). On by default; only you can turn it off.
# protect_git_metadata = false
`

// ---------------------------------------------------------------------------
// version / upgrade / uninstall
// ---------------------------------------------------------------------------

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Printf("corral %s", Version)
			if Commit != "" {
				fmt.Printf(" (%s", Commit)
				if Date != "" {
					fmt.Printf(", %s", Date)
				}
				fmt.Print(")")
			}
			fmt.Printf(" %s/%s\n", runtime.GOOS, runtime.GOARCH)
			lh, _ := paths.LimaHome()
			if lc, err := lima.New(lh); err == nil {
				if v, err := lc.Version(cmd.Context()); err == nil {
					fmt.Printf("lima %s (%s)\n", v, lc.Bin)
				}
			}
			return nil
		},
	}
}

// printResolved prints the effective configuration, one line per key. Every
// key in policy.FileKeys() must appear here — TestResolvedViewShowsEveryKey
// fails otherwise, so a key can never be silently absent from the view.
func printResolved(w io.Writer, cfg *config.Config) {
	ui.KV(w, "profile", cfg.Profile+"  ("+policy.ProfileGuarantee(cfg.Profile)+")")
	ui.KV(w, "default_agent", cfg.DefaultAgent)
	ui.KV(w, "cpus / memory / disk", fmt.Sprintf("%d / %s / %s", cfg.CPUs, cfg.Memory, cfg.Disk))
	ui.KV(w, "yolo", fmt.Sprint(cfg.Yolo))
	ui.KV(w, "stop_on_exit", fmt.Sprint(cfg.StopOnExit))
	ui.KV(w, "readonly_project", fmt.Sprint(cfg.ReadonlyProject))
	ui.KV(w, "agent_state", cfg.AgentState)
	ui.KV(w, "git_identity", fmt.Sprint(cfg.GitIdentity))
	ui.KV(w, "ssh_agent", fmt.Sprint(cfg.SSHAgent))
	ui.KV(w, "no_env_passthrough", fmt.Sprint(cfg.NoEnvPassthrough))
	ui.KV(w, "protect_git_metadata", fmt.Sprint(cfg.ProtectGitMetadata))
	ui.KV(w, "hide", joinNames(cfg.Hide))
	var bd []string
	for _, d := range cfg.BoxDirs {
		if _, err := policy.BoxDirPath(d); err != nil {
			bd = append(bd, d+ui.Bad.Render("  ✗ refused: "+err.Error()))
			continue
		}
		bd = append(bd, d)
	}
	ui.KV(w, "box_dirs", joinNames(bd))
	ui.KV(w, "network", cfg.Network)
	ui.KV(w, "egress", joinNames(cfg.Egress)+ui.Subtle.Render("  (broker mode; git_tokens hosts added automatically)"))
	var apis []string
	for _, ab := range cfg.APIBrokers {
		apis = append(apis, fmt.Sprintf("%s → %s ← %s (%d rule(s))", ab.Name, ab.Upstream, ab.Token, len(ab.Allow)))
	}
	ui.KV(w, "api_brokers", joinNames(apis))
	ui.KV(w, "rosetta", fmt.Sprint(cfg.Rosetta))
	ui.KV(w, "idle_stop", idleStopString(cfg.IdleStop))
	ui.KV(w, "golden", fmt.Sprint(cfg.Golden))
	ui.KV(w, "source", cfg.Source)
	ui.KV(w, "snapshot", cfg.Snapshot)
	ui.KV(w, "snapshots_keep", fmt.Sprint(cfg.SnapshotsKeep))
	fwd := joinNames(cfg.ForwardEnv)
	if cfg.NoEnvPassthrough {
		why := "no_env_passthrough = true"
		if cfg.Profile == config.ProfileStrict {
			why = "profile \"strict\" sets no_env_passthrough"
		}
		fwd += ui.Bad.Render("  ✗ suppressed: "+why) + ui.Subtle.Render("  (agent credential variables too; keychain_env / env_from_host / env / env_file still apply)")
	}
	ui.KV(w, "forward_env", fwd)
	ui.KV(w, "env", joinNames(cfg.Env))
	ui.KV(w, "env_from_host", joinNames(cfg.EnvFromHost))
	ui.KV(w, "keychain_env", joinNames(cfg.KeychainEnv))
	envFile := ui.Subtle.Render("none")
	if cfg.EnvFile != "" {
		envFile = ui.ShortenHome(cfg.EnvFile)
		if _, err := box.LoadEnvFile(cfg.EnvFile); err != nil {
			envFile += ui.Bad.Render("  ✗ " + err.Error())
		}
	}
	ui.KV(w, "env_file", envFile)
	maxRunning := ui.Subtle.Render("no limit")
	if cfg.MaxRunning > 0 {
		maxRunning = fmt.Sprint(cfg.MaxRunning)
	}
	ui.KV(w, "max_running", maxRunning)
	ui.KV(w, "memory_reserve", cfg.MemoryReserve+ui.Subtle.Render("  (kept free for macOS when a box starts)"))
	timeout := ui.Subtle.Render("none")
	if cfg.Timeout != "" {
		timeout = cfg.Timeout
	}
	ui.KV(w, "timeout", timeout)
	var gt []string
	for h, v := range cfg.GitTokens {
		gt = append(gt, h+"←"+v.String())
	}
	ui.KV(w, "git_tokens", joinNames(gt))
	ui.KV(w, "toolchains", joinNames(cfg.Toolchains))
	var tv []string
	for k, v := range cfg.ToolchainVersions {
		tv = append(tv, k+"="+v)
	}
	sort.Strings(tv)
	ui.KV(w, "toolchain_versions", joinNames(tv))
	ui.KV(w, "packages", joinNames(cfg.Packages))
	var ms []string
	for _, m := range cfg.Mounts {
		ms = append(ms, m.String())
	}
	ui.KV(w, "mounts", joinNames(ms))
	ui.KV(w, "provision", joinNames(cfg.Provision))
	name := cfg.Name
	if name == "" {
		name = ui.Subtle.Render("derived from the project path")
	}
	ui.KV(w, "name", name)
}

func newUpgradeCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "upgrade",
		Short:   "Upgrade corral (Homebrew tap or source checkout) and Lima",
		GroupID: "insight",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			self, _ := os.Executable()
			self, _ = filepath.EvalSymlinks(self)
			var err error
			switch {
			case strings.Contains(self, "/Cellar/"):
				err = runVisible(ctx, "brew", "upgrade", "corral-sh/tap/corral", "lima")
			default:
				src := installSource()
				if src == "" {
					return fmt.Errorf("cannot determine how corral was installed; re-run install.sh from a fresh checkout")
				}
				ui.Step(os.Stdout, "updating source checkout %s", ui.ShortenHome(src))
				if err = runVisible(ctx, "git", "-C", src, "pull", "--ff-only"); err == nil {
					err = runVisible(ctx, filepath.Join(src, "install.sh"))
				}
			}
			if err != nil {
				return err
			}
			// Housekeeping an unattended host never gets to do by hand:
			// goldens no box references are cache, not state.
			pruneOrphanGoldens(ctx)
			return nil
		},
	}
}

func newUninstallCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "uninstall",
		Short:   "Delete every box and Corral state (agent logins included)",
		GroupID: "insight",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			h, err := paths.Home()
			if err != nil {
				return err
			}
			if !yes && !ui.Confirm(os.Stderr, fmt.Sprintf("Delete all boxes and %s (including stored agent logins)?", ui.ShortenHome(h)), false) {
				return nil
			}
			metas, _ := box.AllMeta()
			for _, m := range metas {
				if b, err := openBoxByName(m.Name); err == nil {
					_ = ui.RunWithProgress(ctx, "Deleting "+b.Name, func(r func(string)) error { return b.Delete(ctx, r) })
				}
			}
			if err := os.RemoveAll(h); err != nil {
				return err
			}
			ui.Success(os.Stdout, "removed %s. Remove the binary with `brew uninstall corral-sh/tap/corral` or `rm $(which corral)`.", ui.ShortenHome(h))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

// installSource reads ~/.corral/install.json written by install.sh.
func installSource() string {
	h, err := paths.Home()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(h, "install.json"))
	if err != nil {
		return ""
	}
	var v struct {
		Source string `json:"source"`
	}
	if json.Unmarshal(data, &v) != nil {
		return ""
	}
	if _, err := os.Stat(v.Source); err != nil {
		return ""
	}
	return v.Source
}

func runVisible(ctx context.Context, name string, args ...string) error {
	c := exec.CommandContext(ctx, name, args...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}

// diskGiB returns free and total GiB of the volume holding path (-1, 0 when
// df cannot say).
func diskGiB(path string) (free, total float64) {
	out, err := exec.CommandContext(context.Background(), "df", "-g", path).Output()
	if err != nil {
		return -1, 0
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return -1, 0
	}
	f := strings.Fields(lines[len(lines)-1])
	if len(f) < 4 {
		return -1, 0
	}
	free, err = strconv.ParseFloat(f[3], 64)
	if err != nil {
		return -1, 0
	}
	total, _ = strconv.ParseFloat(f[1], 64)
	return free, total
}

// diskReclaimHint names the largest goldens and boxes and the commands that
// free them — orphan goldens first, because they cost nothing to remove.
func diskReclaimHint() string {
	type item struct {
		name string
		size int64
		cmd  string
	}
	var items []item
	if names, bytes, err := orphanGoldens(false); err == nil {
		for _, n := range names {
			items = append(items, item{n, bytes[n], "corral golden prune"})
		}
	}
	if metas, err := box.AllMeta(); err == nil {
		lh, _ := paths.LimaHome()
		if lc, err := lima.New(lh); err == nil {
			for _, m := range metas {
				if !m.IsGolden() {
					items = append(items, item{m.Name, lima.DiskUsage(lc.InstanceDir(m.Name)), "corral delete " + m.Name})
				}
			}
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].size > items[j].size })
	if len(items) > 3 {
		items = items[:3]
	}
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, fmt.Sprintf("%s %s (%s)", it.name, ui.HumanBytes(it.size), it.cmd))
	}
	if len(parts) == 0 {
		return "free disk space; `corral golden prune --dry-run` and `corral list` show what Corral holds"
	}
	return "largest: " + strings.Join(parts, " · ")
}

// parseSizeGiB converts "4GiB"/"512MiB" to GiB for display.
func parseSizeGiB(s string) (float64, bool) {
	m := regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)(GiB|MiB|GB|MB|G|M)$`).FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	f, _ := strconv.ParseFloat(m[1], 64)
	if m[2] == "MiB" || m[2] == "MB" || m[2] == "M" {
		f /= 1024
	}
	return f, true
}
