package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/corral-sh/corral/internal/box"
	"github.com/corral-sh/corral/internal/paths"
	"github.com/corral-sh/corral/internal/ui"
)

// editorCLIs maps --editor values to the host CLI that opens a Remote-SSH window.
var editorCLIs = map[string]string{"code": "code", "cursor": "cursor", "jetbrains": ""}

func newCodeCmd() *cobra.Command {
	var editor string
	var yes bool
	cmd := &cobra.Command{
		Use:     "code",
		Short:   "Open the project inside the box in VS Code (or Cursor / JetBrains) over SSH",
		GroupID: "agents",
		Long: `Attaches an editor to the box's Linux side over the SSH config Lima maintains,
so you edit exactly what the agent sees — the everyday workflow for
source = "clone", and a way to inspect the guest in mount mode.

The editor connects to the ssh_config alias lima-<box>. To make that alias
visible to editors, corral keeps ~/.corral/ssh/config (one Include per
box, pointing at Lima's live ssh.config) and, with your consent, adds a single
Include of that file to ~/.ssh/config. Nothing else in ~/.ssh is touched.`,
		Example: "  corral code\n  corral code --editor cursor\n  corral code --editor jetbrains   # prints the Gateway settings",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, ok := editorCLIs[editor]; !ok {
				return fmt.Errorf("unknown --editor %q (code, cursor, jetbrains)", editor)
			}
			b, err := openBox()
			if err != nil {
				return err
			}
			if err := ensureRunning(cmd.Context(), b); err != nil {
				return err
			}
			if err := ensureSSHInclude(b, yes); err != nil {
				return err
			}
			host := box.SSHHost(b.Name)
			workdir := b.Project
			if editor == "jetbrains" {
				fmt.Println(ui.Header.Render("JetBrains Gateway → SSH connection"))
				ui.KV(os.Stdout, "host", host+"  (from ~/.ssh/config)")
				ui.KV(os.Stdout, "project path", workdir)
				fmt.Println(ui.Subtle.Render("  Gateway: New connection → SSH → host " + host + " (user/key/port come from the config) → open " + workdir))
				return nil
			}
			cli := editorCLIs[editor]
			if _, err := exec.LookPath(cli); err != nil {
				return fmt.Errorf("%s CLI not on PATH — in the editor run \"Shell Command: Install '%s' command in PATH\", or connect manually to ssh host %s", cli, cli, host)
			}
			ui.Step(os.Stdout, "opening %s in %s (ssh %s)", ui.ShortenHome(workdir), cli, host)
			return runVisible(cmd.Context(), cli, "--remote", "ssh-remote+"+host, workdir)
		},
	}
	cmd.Flags().StringVar(&editor, "editor", "code", "code | cursor | jetbrains")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "add the Include to ~/.ssh/config without asking")
	return cmd
}

// ensureSSHInclude refreshes ~/.corral/ssh/config for every known box and
// makes sure ~/.ssh/config includes it — asking first, because ~/.ssh is the
// user's, not ours.
func ensureSSHInclude(b *box.Box, yes bool) error {
	inc, err := paths.SSHIncludeFile()
	if err != nil {
		return err
	}
	metas, err := box.AllMeta()
	if err != nil {
		return err
	}
	names := []string{b.Name}
	for _, m := range metas {
		if m.Name != b.Name && !m.Golden {
			names = append(names, m.Name)
		}
	}
	if err := box.WriteSSHInclude(inc, b.Lima.LimaHome, names); err != nil {
		return fmt.Errorf("write %s: %w", inc, err)
	}
	uh, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	userCfg := filepath.Join(uh, ".ssh", "config")
	has, err := box.HasUserSSHInclude(userCfg, inc)
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	if !yes {
		if !ui.IsTTY() {
			return fmt.Errorf("~/.ssh/config does not include %s; re-run with --yes to add the Include, or add `Include %q` yourself", ui.ShortenHome(inc), inc)
		}
		q := fmt.Sprintf("Add `Include %s` to ~/.ssh/config so editors can reach boxes as ssh lima-<box>?", ui.ShortenHome(inc))
		if !ui.Confirm(os.Stderr, q, true) {
			return fmt.Errorf("not changing ~/.ssh/config; add `Include %q` yourself and re-run", inc)
		}
	}
	if _, err := box.EnsureUserSSHInclude(userCfg, inc); err != nil {
		return fmt.Errorf("update %s: %w", userCfg, err)
	}
	ui.Success(os.Stdout, "added Include of %s to ~/.ssh/config", ui.ShortenHome(inc))
	box.Audit(box.AuditEvent{Event: "ssh-config-include", Box: b.Name, Project: b.Project, Argv: []string{inc}})
	return nil
}
