package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/corral-sh/corral/internal/agent"
	"github.com/corral-sh/corral/internal/config"
	"github.com/corral-sh/corral/internal/paths"
	"github.com/corral-sh/corral/internal/policy"
	"github.com/corral-sh/corral/internal/ui"
)

func newSetupCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "setup",
		Short:   "Interactive first-time setup: prerequisites, defaults, login",
		GroupID: "insight",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			ui.Banner(os.Stdout, Version)
			fmt.Println()

			// 1. Prerequisites
			fmt.Println(ui.Header.Render("1. Host prerequisites"))
			checks := hostChecks(ctx)
			for _, c := range checks {
				mark := ui.Ok.Render("✓")
				if !c.ok {
					mark = ui.Bad.Render("✗")
				}
				fmt.Printf("  %s %-24s %s\n", mark, c.name, ui.Subtle.Render(c.detail))
				if !c.ok && strings.HasPrefix(c.name, "Lima installed") {
					if ui.Confirm(os.Stderr, "Install Lima with Homebrew now?", true) {
						if err := brewInstallLima(ctx); err != nil {
							return err
						}
					}
				} else if !c.ok && c.fix != "" {
					fmt.Printf("      %s %s\n", ui.Warn.Render("fix:"), c.fix)
				}
			}
			fmt.Println()

			// 2. Defaults form
			gp, err := paths.GlobalConfigFile()
			if err != nil {
				return err
			}
			existing, err := policy.Load(gp, "", "")
			if err != nil {
				return err
			}
			fmt.Println(ui.Header.Render("2. Defaults for new boxes") + ui.Subtle.Render("  → "+ui.ShortenHome(gp)))
			if !ui.IsTTY() {
				return fmt.Errorf("setup needs an interactive terminal")
			}

			cpus := strconv.Itoa(existing.CPUs)
			memory := existing.Memory
			disk := existing.Disk
			toolchains := append([]string{}, existing.Toolchains...)
			defaultAgent := existing.DefaultAgent
			stopOnExit := existing.StopOnExit
			gitIdentity := existing.GitIdentity
			yolo := existing.Yolo
			gitHost, gitTokenVar, gitUser := "", "", ""
			for h, v := range existing.GitTokens { // first configured host pre-fills the form; a configured user is kept
				gitHost, gitTokenVar, gitUser = h, v.Token, v.User
				break
			}
			envForward := strings.Join(existing.Env, ",")

			var agentOpts []huh.Option[string]
			for _, a := range agent.All() {
				agentOpts = append(agentOpts, huh.NewOption(a.Name()+" — "+a.Summary(), a.Name()))
			}
			var tcOpts []huh.Option[string]
			for _, t := range config.KnownToolchains {
				label := t
				switch t {
				case "node":
					label = "node — Node.js LTS from nodejs.org (checksum-verified)"
				case "go":
					label = "go — latest stable Go from go.dev (checksum-verified)"
				case "python":
					label = "python — Python 3 + pipx from Ubuntu"
				case "docker":
					label = "docker — Docker Engine inside the box (never the host socket)"
				}
				tcOpts = append(tcOpts, huh.NewOption(label, t))
			}

			form := huh.NewForm(
				huh.NewGroup(
					huh.NewSelect[string]().Title("Default agent").Options(agentOpts...).Value(&defaultAgent),
					huh.NewInput().Title("CPUs per box").Value(&cpus).Validate(func(s string) error {
						n, err := strconv.Atoi(s)
						if err != nil || n < 1 || n > 64 {
							return fmt.Errorf("enter a number between 1 and 64")
						}
						return nil
					}),
					huh.NewInput().Title("Memory per box").Description("e.g. 8GiB — Claude Code wants ≥ 4GiB").Value(&memory),
					huh.NewInput().Title("Disk per box").Description("sparse: only used space is consumed on your Mac").Value(&disk),
				),
				huh.NewGroup(
					huh.NewMultiSelect[string]().Title("Toolchains to preinstall").Options(tcOpts...).Value(&toolchains),
					huh.NewConfirm().Title("Skip agent permission prompts inside the box?").
						Description("The VM is the safety boundary. Choose No to keep Claude's own prompts.").Value(&yolo),
					huh.NewConfirm().Title("Forward your git user.name / user.email into the box?").Value(&gitIdentity),
					huh.NewConfirm().Title("Stop the box when a session ends?").Description("Saves RAM; next start takes ~20 s").Value(&stopOnExit),
					huh.NewInput().Title("Git host to authenticate from the box (optional)").
						Description("e.g. git.example.com or github.com — enables HTTPS pushes; leave empty to skip").Value(&gitHost),
					huh.NewInput().Title("Host env var holding the token for that git host").
						Description("e.g. GITLAB_TOKEN — the value is read from your shell at launch, never stored").Value(&gitTokenVar),
					huh.NewInput().Title("Extra host env vars to forward (comma-separated, optional)").
						Description("Names only, e.g. GITLAB_TOKEN,JIRA_TOKEN — forwarded when set on your Mac").Value(&envForward),
				),
			).WithTheme(huh.ThemeCharm())
			if err := form.Run(); err != nil {
				return err
			}

			n, _ := strconv.Atoi(cpus)
			f := config.File{
				DefaultAgent: &defaultAgent,
				CPUs:         &n,
				Memory:       &memory,
				Disk:         &disk,
				Toolchains:   toolchains,
				Yolo:         &yolo,
				GitIdentity:  &gitIdentity,
				StopOnExit:   &stopOnExit,
			}
			if h, v := strings.TrimSpace(gitHost), strings.TrimSpace(gitTokenVar); h != "" && v != "" {
				f.GitTokens = map[string]config.GitToken{h: {Token: v, User: gitUser}}
			} else if h != "" || v != "" {
				return fmt.Errorf("git host and token variable must be given together")
			}
			for _, e := range strings.Split(envForward, ",") {
				if e = strings.TrimSpace(e); e != "" {
					f.Env = append(f.Env, e)
				}
			}
			if _, err := config.Resolve(config.Merge(config.Defaults(), f)); err != nil {
				return err
			}
			if err := config.WriteGlobal(gp, f); err != nil {
				return err
			}
			ui.Success(os.Stdout, "saved %s", ui.ShortenHome(gp))
			fmt.Println()

			// 3. Next steps
			fmt.Println(ui.Header.Render("3. Next"))
			if a, ok := agent.Lookup(defaultAgent); ok {
				fmt.Printf("  %s cd into a project and run %s\n", ui.Info.Render("→"), ui.Code.Render("corral "+a.Name()))
				fmt.Printf("  %s %s\n", ui.Info.Render("→"), ui.Subtle.Render(a.LoginHint()))
			}
			fmt.Printf("  %s %s shows every box; %s explains the security model\n", ui.Info.Render("→"), ui.Code.Render("corral"), ui.Code.Render("corral info"))
			return nil
		},
	}
}
