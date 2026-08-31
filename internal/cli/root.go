// Package cli wires the Corral commands together.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/corral-sh/corral/internal/agent"
	"github.com/corral-sh/corral/internal/box"
	"github.com/corral-sh/corral/internal/config"
	"github.com/corral-sh/corral/internal/paths"
	"github.com/corral-sh/corral/internal/policy"
	"github.com/corral-sh/corral/internal/ui"
)

// Version is set at build time via -ldflags.
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

// globalFlags are accepted by every command that touches a box.
type globalFlags struct {
	yes      bool // --yes: create a missing box without asking
	noCreate bool // --no-create: refuse to create a box (wrong directory guard)
	project  string
	name     string
	offline  bool   // --offline on launch commands: network = "offline"
	profile  string // --profile: raise the profile floor for this launch
	noGolden bool   // --no-golden: build the box from scratch instead of cloning
	repo     string // --repo URL[@ref]: clone mode without a local checkout
}

var gf globalFlags

// Execute runs the CLI.
func Execute() int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	root := newRoot()
	if err := root.ExecuteContext(ctx); err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			if ee.msg != "" {
				ui.Failure(os.Stderr, "%s", ee.msg)
			}
			return ee.code
		}
		ui.Failure(os.Stderr, "%s", err.Error())
		return 1
	}
	return 0
}

// exitError carries a specific exit code out of a command. With no msg it
// propagates the guest command's own status silently; with one it is an
// Corral outcome (see outcome.go) and the message is printed.
type exitError struct {
	code    int
	outcome string
	msg     string
}

func (e *exitError) Error() string {
	if e.msg != "" {
		return e.msg
	}
	return fmt.Sprintf("exit %d", e.code)
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "corral",
		Short: ui.Logo + " Corral — your agent, unleashed. Your Mac, untouched.",
		Long: ui.Logo + " " + ui.Title.Render("Corral") + `

Runs AI coding agents (Claude Code today, more later) inside a lightweight
Linux VM built on Lima and Apple's Virtualization framework. Your project is
mounted at its real path; your Mac home directory, SSH keys and Keychain are
not. The agent can go full-send inside the box.

  cd ~/Code/my-project
  corral claude              # first run builds the box (~3 min), later runs take 10–25 s

Run ` + ui.Code.Render("corral") + ` with no arguments for the dashboard.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDashboard(cmd.Context())
		},
		CompletionOptions: cobra.CompletionOptions{HiddenDefaultCmd: true},
	}
	root.PersistentFlags().StringVarP(&gf.project, "project", "C", "", "project directory (default: current directory)")
	root.PersistentFlags().StringVar(&gf.name, "box", "", "override the box name (default: derived from the project path)")
	root.PersistentFlags().StringVar(&gf.repo, "repo", "", "clone this repository inside the box instead of mounting a checkout: URL[@ref] (source = \"clone\")")

	for _, a := range agent.All() {
		root.AddCommand(newAgentCmd(a))
	}
	root.AddCommand(
		newShellCmd(),
		newCodeCmd(),
		newEgressCmd(),
		newBrokerCmd(),
		newRunCmd(),
		newUpCmd(),
		newStopCmd(),
		newGCCmd(),
		newGoldenCmd(),
		newStartCmd(),
		newRestartCmd(),
		newRebuildCmd(),
		newDeleteCmd(),
		newListCmd(),
		newInfoCmd(),
		newLogsCmd(),
		newSnapshotCmd(),
		newUndoCmd(),
		newSetupCmd(),
		newConfigCmd(),
		newDoctorCmd(),
		newDocsCmd(),
		newAgentsCmd(),
		newAuditCmd(),
		newVersionCmd(),
		newUpgradeCmd(),
		newUninstallCmd(),
	)
	root.SetHelpCommandGroupID("")
	root.AddGroup(
		&cobra.Group{ID: "agents", Title: "Agents:"},
		&cobra.Group{ID: "box", Title: "Box lifecycle:"},
		&cobra.Group{ID: "insight", Title: "Insight & setup:"},
	)
	return root
}

// projectDir resolves the project directory from flags or cwd.
func projectDir() (string, error) {
	if gf.project != "" {
		return gf.project, nil
	}
	return os.Getwd()
}

// loadConfig merges global + project + host per-project config and applies
// the flag overrides. boxName is the effective box name when the caller knows
// it (--box, an existing box's metadata); "" means derive it from the project.
// The host per-project file is keyed on that effective name: a box named
// with --box reads ~/.corral/projects/<that name>.toml, everything else the
// file of the derived name — so a trusted setting can never be skipped just
// because the box was named explicitly.
func loadConfig(project, boxName string) (*config.Config, error) {
	gp, err := paths.GlobalConfigFile()
	if err != nil {
		return nil, err
	}
	hp, err := hostProjectConfig(project, effectiveBoxName(project, boxName, gp))
	if err != nil {
		return nil, err
	}
	cfg, err := policy.Load(gp, hp, project)
	if err != nil {
		return nil, err
	}
	if boxName != "" {
		cfg.Name = boxName
	}
	if gf.offline {
		cfg.Network = config.NetworkOffline
	}
	if gf.noGolden {
		cfg.Golden = false
	}
	if gf.repo != "" {
		cfg.Source = config.SourceClone
	}
	if gf.profile != "" {
		if config.ProfileRank(gf.profile) < 0 {
			return nil, fmt.Errorf("--profile %q must be one of %s", gf.profile, strings.Join(config.Profiles, ", "))
		}
		if config.ProfileRank(gf.profile) > config.ProfileRank(cfg.Profile) {
			cfg.Profile = gf.profile
		}
	}
	// Flags can tighten but never loosen below the profile's floor.
	policy.ApplyProfile(cfg)
	return cfg, nil
}

// effectiveBoxName is the name the host per-project file is keyed on: the
// explicit name when given, else `name =` from the global/project layers, else
// "" (derive from the project path).
func effectiveBoxName(project, explicit, globalPath string) string {
	if explicit != "" {
		return explicit
	}
	if cfg, err := policy.Load(globalPath, "", project); err == nil && cfg.Name != "" {
		return cfg.Name
	}
	return ""
}

// hostProjectConfig returns ~/.corral/projects/<box>.toml for the effective
// box name; "" derives the name from the project path.
func hostProjectConfig(project, boxName string) (string, error) {
	if boxName == "" {
		var err error
		if boxName, err = box.DefaultNameFor(project); err != nil {
			return "", err
		}
	}
	return paths.ProjectConfigFile(boxName)
}

// openBox resolves config and box for the current project.
func openBox() (*box.Box, error) {
	project, err := projectDir()
	if err != nil {
		return nil, err
	}
	if gf.repo != "" {
		// No checkout: a placeholder directory gives the box its identity.
		rd, err := paths.ReposDir()
		if err != nil {
			return nil, err
		}
		project = box.RepoProjectDir(rd, gf.repo)
		if err := os.MkdirAll(project, 0o700); err != nil {
			return nil, err
		}
	}
	cfg, err := loadConfig(project, gf.name)
	if err != nil {
		return nil, err
	}
	b, err := box.Open(project, cfg, Version)
	if err != nil {
		return nil, err
	}
	b.Repo = gf.repo
	if b.Repo == "" && b.Meta != nil && b.Meta.Repo != "" {
		// A --repo box found again without the flag (rebuild, info, delete).
		b.Repo = b.Meta.Repo
		b.Cfg.Source = config.SourceClone
	}
	return b, nil
}

// openBoxByName finds an existing box from metadata (used by lifecycle
// commands that accept a box name instead of running from the project).
func openBoxByName(name string) (*box.Box, error) {
	m, err := box.LoadMeta(name)
	if err != nil {
		return nil, fmt.Errorf("unknown box %q (see `corral list`)", name)
	}
	cfg, err := loadConfig(m.Project, name)
	if err != nil {
		// The project may have moved; fall back to defaults so lifecycle ops still work.
		cfg, _ = config.Resolve(config.Defaults())
	}
	cfg.Name = name
	if m.Repo != "" {
		cfg.Source = config.SourceClone
	}
	if _, statErr := os.Stat(m.Project); statErr != nil {
		// Project gone: build a Box without path validation.
		lh, err := paths.LimaHome()
		if err != nil {
			return nil, err
		}
		b, err := box.OpenDetached(name, m.Project, cfg, lh, Version)
		if err != nil {
			return nil, err
		}
		return b, nil
	}
	b, err := box.Open(m.Project, cfg, Version)
	if err != nil {
		return nil, err
	}
	b.Repo = m.Repo
	return b, nil
}

// resolveBoxArg returns the box named by args[0] or the current project's box.
func resolveBoxArg(args []string) (*box.Box, error) {
	if len(args) > 0 {
		return openBoxByName(args[0])
	}
	return openBox()
}

func joinNames(items []string) string {
	if len(items) == 0 {
		return ui.Subtle.Render("none")
	}
	return strings.Join(items, ", ")
}
