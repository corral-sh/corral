package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/corral-sh/corral/internal/agent"
	"github.com/corral-sh/corral/internal/box"
	"github.com/corral-sh/corral/internal/config"
	"github.com/corral-sh/corral/internal/lima"
	"github.com/corral-sh/corral/internal/policy"
	"github.com/corral-sh/corral/internal/ui"
)

// launchFlags are shared by agent shortcuts, shell and run.
type launchFlags struct {
	ask          bool
	dryRun       bool
	stopOnExit   bool
	noStopOnExit bool
	importConfig bool
	preflight    bool
	timeout      string // duration; "" = config `timeout` or none
	result       string // --result <file>
}

func addLaunchFlags(cmd *cobra.Command, lf *launchFlags, isAgent bool) {
	if isAgent {
		cmd.Flags().BoolVar(&lf.ask, "ask", false, "keep the agent's own permission prompts (default: skip them — the box is the boundary)")
		cmd.Flags().BoolVar(&lf.importConfig, "import-config", false, "copy your host agent settings/skills (never credentials) into the box state first")
	}
	cmd.Flags().BoolVar(&lf.dryRun, "dry-run", false, "show what would run (box, mounts, forwarded variable names) without starting anything")
	cmd.Flags().BoolVar(&lf.stopOnExit, "stop-on-exit", false, "stop the box when the session ends")
	cmd.Flags().BoolVar(&gf.noGolden, "no-golden", false, "build the box from scratch instead of cloning the golden image")
	cmd.Flags().StringVar(&gf.profile, "profile", "", "raise the profile for this launch: offline | strict (never lower than config)")
	cmd.Flags().BoolVar(&gf.offline, "offline", false, "network = \"offline\": reject egress inside the box and drop sudo (applies on create/rebuild)")
	cmd.Flags().BoolVar(&lf.noStopOnExit, "keep-running", false, "leave the box running when the session ends (overrides config)")
	cmd.Flags().BoolVar(&lf.preflight, "preflight", false, "run `corral doctor <box>` first and refuse to start if a declared grant does not work from inside the box (exit 78)")
	cmd.Flags().StringVar(&lf.timeout, "timeout", "", "end the session after this long, e.g. 45m (SIGTERM, then SIGKILL after 10 s); exit 124. Default: config `timeout`")
	cmd.Flags().StringVar(&lf.result, "result", "", "write a JSON record of the outcome (box, agent, outcome, exit_code, reason, duration, forwarded variable names) to this file")
	addCreateFlags(cmd)
}

// addCreateFlags guards against the 3 GB accident of running in the wrong
// directory: the first run in a project asks before creating (TTY only).
func addCreateFlags(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&gf.yes, "yes", false, "create the box without asking when this project has none yet")
	cmd.Flags().BoolVar(&gf.noCreate, "no-create", false, "refuse to create a box: fail if this project has none yet")
}

func newAgentCmd(a agent.Agent) *cobra.Command {
	lf := &launchFlags{}
	cmd := &cobra.Command{
		Use:     a.Name() + " [-- agent args...]",
		Short:   "Launch " + a.Summary(),
		GroupID: "agents",
		Long: fmt.Sprintf(`Launch %s inside the box for the current project.

The first run in a project creates the box (downloads a digest-pinned Ubuntu
image, installs toolchains and the agent). Later runs boot it in 10–25 seconds.
Arguments after the agent name are passed through, e.g.

  corral %s -- -p "summarise this repo"
  corral %s --ask            # keep the agent's permission prompts

Authentication: %s`, a.Name(), a.Name(), a.Name(), a.LoginHint()),
		DisableFlagParsing: false,
		FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
		RunE: func(cmd *cobra.Command, args []string) error {
			// Unknown flags belong to the agent: cobra leaves them in args
			// when UnknownFlags is whitelisted only for the values; rebuild
			// the passthrough list from the raw command line instead.
			passthrough := agentPassthroughArgs(cmd, os.Args)
			return launch(cmd.Context(), a, passthrough, lf)
		},
	}
	addLaunchFlags(cmd, lf, true)
	return cmd
}

// agentPassthroughArgs returns everything after the agent sub-command that is
// not one of our own flags. A literal "--" ends our flag parsing explicitly.
func agentPassthroughArgs(cmd *cobra.Command, argv []string) []string {
	idx := -1
	for i, a := range argv {
		if a == cmd.Name() {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	rest := argv[idx+1:]
	var out []string
	// Our flags, derived from what is registered (own + persistent) so a new
	// flag can never leak into the agent's argv. takesValue: "--flag value".
	own := map[string]bool{}
	takesValue := map[string]bool{}
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		own["--"+f.Name] = true
		if f.Shorthand != "" {
			own["-"+f.Shorthand] = true
		}
		if f.NoOptDefVal == "" { // no implicit value ⇒ the next token is the value
			takesValue["--"+f.Name] = true
			if f.Shorthand != "" {
				takesValue["-"+f.Shorthand] = true
			}
		}
	})
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		if a == "--" {
			out = append(out, rest[i+1:]...)
			break
		}
		if k, _, hasEq := strings.Cut(a, "="); own[k] {
			if !hasEq && takesValue[k] && i+1 < len(rest) {
				i++
			}
			continue
		}
		out = append(out, a)
	}
	return out
}

func newShellCmd() *cobra.Command {
	lf := &launchFlags{}
	cmd := &cobra.Command{
		Use:     "shell",
		Short:   "Open an interactive shell inside the project's box",
		GroupID: "agents",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return launch(cmd.Context(), nil, []string{"bash", "-l"}, lf)
		},
	}
	addLaunchFlags(cmd, lf, false)
	return cmd
}

func newRunCmd() *cobra.Command {
	lf := &launchFlags{}
	cmd := &cobra.Command{
		Use:     "run <command> [args...]",
		Short:   "Run a single command inside the project's box",
		GroupID: "agents",
		Example: "  corral run make test\n  corral run -- npm ci",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return launch(cmd.Context(), nil, args, lf)
		},
	}
	addLaunchFlags(cmd, lf, false)
	return cmd
}

func newUpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "up",
		Short:   "Create/start the project's box without launching anything",
		GroupID: "box",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			b, err := openBox()
			if err != nil {
				return err
			}
			if err := ensureRunning(cmd.Context(), b); err != nil {
				return err
			}
			ui.Success(os.Stdout, "%s is running · project %s", ui.Bold.Render(b.Name), ui.ShortenHome(b.Project))
			return nil
		},
	}
	addCreateFlags(cmd)
	return cmd
}

// checkProvision refuses to continue when a repository provision script failed
// during this boot: a box with a missing dependency or control must not start
// a session that then improvises around it.
func checkProvision(ctx context.Context, b *box.Box) error {
	if len(b.Cfg.Provision) == 0 {
		return nil
	}
	fails, err := b.ProvisionFailures(ctx)
	if err != nil || len(fails) == 0 {
		return nil //nolint:nilerr // a box we cannot query is reported by the next step, not here
	}
	return fmt.Errorf("%s — see `corral logs %s`; fix the script (it runs as the box user unless it contains `# corral: system`), then `corral rebuild`", strings.Join(fails, "; "), b.Name)
}

// shouldConfirmCreate: a first run asks before building a box, unless the
// caller said --yes or runs non-interactively (CORRAL_PLAIN=1 — scripts,
// make e2e). Confirm itself is a no-op without a TTY.
func shouldConfirmCreate(yes bool, plain string) bool {
	return !yes && plain != "1"
}

// launch is the shared path for agent shortcuts, shell and run.
func launch(ctx context.Context, a agent.Agent, argv []string, lf *launchFlags) error {
	b, err := openBox()
	if err != nil {
		return err
	}
	yolo := b.Cfg.Yolo && !lf.ask
	spec, err := b.BuildLaunch(a, argv, yolo, box.HostEnvMap(os.Environ()))
	if err != nil {
		return err
	}
	for _, w := range spec.Warnings {
		ui.Warning(os.Stderr, "%s", w)
	}

	if lf.dryRun {
		return printDryRun(b, a, spec)
	}
	if lf.importConfig && a != nil {
		if err := importAgentConfig(a, true); err != nil {
			return err
		}
	}
	agentName := ""
	if a != nil {
		agentName = a.Name()
	}
	timeoutStr := lf.timeout
	if timeoutStr == "" {
		timeoutStr = b.Cfg.Timeout
	}
	timeout, err := config.ParseTimeout(timeoutStr)
	if err != nil {
		return err
	}
	started := time.Now()
	// refuse ends the run before a session existed: the --result record and
	// the audit entry still say why, with the outcome's exit code.
	refuse := func(outcome string, code int, msg string) error {
		box.Audit(box.AuditEvent{Event: "refused", Box: b.Name, Project: b.Project, Agent: agentName, Outcome: outcome, ExitCode: &code})
		writeResult(lf.result, runResult{Box: b.Name, Project: b.Project, Agent: agentName, Outcome: outcome, ExitCode: code, Reason: msg,
			Started: started, Ended: time.Now(), Duration: time.Since(started).Truncate(time.Second).String(), Forwarded: spec.Forwarded})
		return &exitError{code: code, outcome: outcome, msg: msg}
	}

	idleSweep(ctx, b.Name)
	autoSnapshot(ctx, b)
	if _, st, _ := b.Status(ctx); st != box.StateRunning {
		var ee *exitError
		if err := admit(ctx, b); errors.As(err, &ee) {
			return refuse(ee.outcome, ee.code, ee.msg)
		}
	}
	// The session exists from here on, before the box boots: a concurrent idle
	// sweep must never take a booting box for an idle one. Every early
	// return below ends it; the normal path ends it after the command.
	b.SessionStart()
	sessionOpen := true
	defer func() {
		if sessionOpen {
			b.SessionEnd()
		}
	}()
	if err := ensureRunning(ctx, b); err != nil {
		return err
	}
	if lf.preflight {
		if bad := printChecks(os.Stderr, boxPreflight(ctx, b, box.HostEnvMap(os.Environ())), false); bad > 0 {
			return refuse(OutcomePreflightRefused, ExitPreflightRefused, fmt.Sprintf("%d preflight check(s) failed — fix them or start without --preflight", bad))
		}
	}
	if box.NeedsBroker(b.Cfg) && !box.BrokerReady(b.Name) {
		return fmt.Errorf("egress broker for %s is not answering on %s; refusing to start a sealed session — run `corral start %s` (see `corral egress`)", b.Name, box.BrokerAddr(b.Name), b.Name)
	}
	// --timeout: the deadline cancels the SSH process — SIGTERM first so
	// the guest side sees the session end, SIGKILL if it lingers.
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	var deadline <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		deadline = t.C
	}
	cmd, err := b.Lima.SSHCommand(runCtx, b.Name, spec.Workdir, box.SendEnvPatterns(), spec.Argv)
	if err != nil {
		return err
	}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	cmd.Env = spec.ProcessEnv(os.Environ())
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr

	b.Touch()
	start := time.Now()
	box.Audit(box.AuditEvent{Event: "launch", Box: b.Name, Project: b.Project, Agent: agentName, Argv: spec.Argv, Yolo: &yolo, Forwarded: spec.Forwarded})
	if ui.IsTTY() {
		if a != nil {
			// An agent session gets the greeting card; shell/run stay terse.
			mode := "prompts skipped — the box is the boundary"
			if !yolo {
				mode = "agent prompts on (--ask)"
			}
			rows := [][2]string{
				{"project", ui.ShortenHome(b.Project) + ui.Subtle.Render("  ("+sourceWord(b)+")")},
				{"agent", a.Name() + ui.Subtle.Render("  · "+mode)},
				{"network", networkLine(b)},
			}
			if b.Cfg.Profile != config.ProfileDefault {
				rows = append(rows, [2]string{"profile", b.Cfg.Profile})
			}
			ui.Greeting(os.Stderr, b.Name, rows)
		} else {
			fmt.Fprintf(os.Stderr, "%s %s %s\n", ui.Logo, ui.Subtle.Render("entering"), ui.Bold.Render(b.Name)+ui.Subtle.Render(" · "+ui.ShortenHome(b.Project)))
		}
	}
	timedOut := false
	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()
	var runErr error
	select {
	case runErr = <-done:
	case <-deadline:
		timedOut = true
		cancelRun()
		runErr = <-done
		killSession(ctx, b, spec.Env["CORRAL_SESSION"])
	}
	b.SessionEnd()
	sessionOpen = false
	code, outcome, reason := 0, OutcomeExit, ""
	switch {
	case timedOut:
		code, outcome, reason = ExitTimeout, OutcomeTimeout, "session ended by --timeout "+timeoutStr
	case runErr != nil:
		var ee *exec.ExitError
		if errors.As(runErr, &ee) && ee.ExitCode() != 255 {
			code = ee.ExitCode()
		} else {
			// ssh's own failure status (255) or no process at all: the box,
			// not the command, went away.
			code, outcome, reason = ExitUnreachable, OutcomeUnreachable, unreachableReason(ctx, b)
		}
	}
	dur := time.Since(start).Truncate(time.Second).String()
	box.Audit(box.AuditEvent{Event: "exit", Box: b.Name, Agent: agentName, ExitCode: &code, Duration: dur, Outcome: outcome})
	writeResult(lf.result, runResult{Box: b.Name, Project: b.Project, Agent: agentName, Outcome: outcome, ExitCode: code, Reason: reason,
		Started: start, Ended: time.Now(), Duration: dur, Forwarded: spec.Forwarded})

	stop := b.Cfg.StopOnExit
	if lf.stopOnExit {
		stop = true
	}
	if lf.noStopOnExit {
		stop = false
	}
	if stop {
		if err := ui.RunWithProgress(ctx, "Stopping "+b.Name, func(report func(string)) error {
			return b.Lima.Stop(ctx, b.Name, report)
		}); err != nil {
			ui.Warning(os.Stderr, "stop failed: %v", err)
		}
		box.StopBrokerFor(b.Name)
		box.Audit(box.AuditEvent{Event: "stop", Box: b.Name})
	}
	if outcome != OutcomeExit {
		return &exitError{code: code, outcome: outcome, msg: reason}
	}
	if code != 0 {
		return &exitError{code: code}
	}
	return nil
}

// ensureRunning creates or boots the box, with progress UI, and for
// network = "broker" makes sure the egress broker on the Mac is up.
func ensureRunning(ctx context.Context, b *box.Box) error {
	// Callers without a session of their own (up, start, code, doctor) still
	// hold one for the boot, so the box is "in use" while it starts and its
	// idle clock restarts when the boot is done. For launch this is a
	// no-op: its session is already registered and stays open.
	if !b.SessionOpen() {
		b.SessionStart()
		defer b.SessionEnd()
	}
	if err := ensureVM(ctx, b); err != nil {
		return err
	}
	return ensureBroker(ctx, b)
}

// ensureBroker starts the box's egress broker child if the mode needs one.
// It is what makes broker mode fail closed: without it the guest has no route.
func ensureBroker(ctx context.Context, b *box.Box) error {
	if !box.NeedsBroker(b.Cfg) {
		return nil
	}
	if b.Meta == nil {
		_ = b.RecoverMeta(ctx)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := b.StartBroker(ctx, exe); err != nil {
		if b.Cfg.Network == config.NetworkBroker {
			return fmt.Errorf("%w — the box is running but sealed: nothing leaves it until the broker is up (`corral egress` shows the state)", err)
		}
		return fmt.Errorf("%w — the api_brokers routes are unavailable until it is (`corral egress` shows the state)", err)
	}
	return nil
}

func ensureVM(ctx context.Context, b *box.Box) error {
	if err := checkHost(ctx, b); err != nil {
		return err
	}
	_, st, err := b.Status(ctx)
	if err != nil {
		return err
	}
	switch st {
	case box.StateMissing:
		if b.Meta != nil {
			// Metadata without VM: previous delete was partial, or Lima home changed.
			_ = box.DeleteMeta(b.Name)
			b.Meta = nil
		}
		if err := createBox(ctx, b); err != nil {
			return err
		}
		return checkProvision(ctx, b)
	case box.StateStopped:
		if b.Meta == nil {
			_ = b.RecoverMeta(ctx)
		}
		if drifted, _ := b.Drifted(); drifted {
			ui.Warning(os.Stderr, "configuration changed since %s was built — run `corral rebuild` to apply (continuing with the existing box)", b.Name)
		}
		if err := ui.RunWithProgress(ctx, "Starting "+b.Name, func(report func(string)) error {
			return b.Lima.Start(ctx, b.Name, report)
		}); err != nil {
			return err
		}
		return checkProvision(ctx, b)
	default:
		if b.Meta == nil {
			_ = b.RecoverMeta(ctx)
		}
		if drifted, _ := b.Drifted(); drifted {
			ui.Warning(os.Stderr, "configuration changed since %s was built — run `corral rebuild` to apply", b.Name)
		}
		return nil
	}
}

// errNoCreate is returned when --no-create meets a project without a box.
var errNoCreate = errors.New("no box for this project and --no-create was given; run `corral up` from the project directory (or -C <dir>) to create one")

func createBox(ctx context.Context, b *box.Box) error {
	if gf.noCreate {
		return errNoCreate
	}
	if ui.IsTTY() {
		fmt.Fprintf(os.Stderr, "%s %s\n", ui.Logo, ui.Title.Render("Building a new box for "+ui.ShortenHome(b.Project)))
		ui.KV(os.Stderr, "box", b.Name)
		ui.KV(os.Stderr, "resources", fmt.Sprintf("%d CPU · %s RAM · %s disk (sparse)", b.Cfg.CPUs, b.Cfg.Memory, b.Cfg.Disk))
		ui.KV(os.Stderr, "toolchains", joinNames(b.Cfg.Toolchains))
		ui.KV(os.Stderr, "agents", joinNames(agent.Names()))
		if len(b.Cfg.Packages) > 0 {
			ui.KV(os.Stderr, "packages", strings.Join(b.Cfg.Packages, " "))
		}
		mode := "read/write"
		if b.Cfg.ReadonlyProject {
			mode = "read-only"
		}
		if b.Cfg.Source == config.SourceClone {
			mode = "none — cloned inside the box at session start (source = \"clone\")"
		}
		ui.KV(os.Stderr, "project mount", mode)
		if gib, ok := parseSizeGiB(b.Cfg.Memory); ok {
			for _, tc := range b.Cfg.Toolchains {
				switch {
				case tc == "docker" && gib < 6:
					ui.Warning(os.Stderr, "memory = %s with the docker toolchain: containers plus the agent may OOM; consider memory = \"6GiB\"", b.Cfg.Memory)
				case (tc == "android" || tc == "flutter") && gib < 8:
					ui.Warning(os.Stderr, "memory = %s with the %s toolchain: Gradle plus the agent may OOM; consider memory = \"8GiB\"", b.Cfg.Memory, tc)
				}
			}
		}
		if !b.Cfg.Rosetta {
			for _, tc := range b.Cfg.Toolchains {
				if tc == "android" || tc == "flutter" {
					ui.Warning(os.Stderr, "the %s toolchain's build tools (aapt2) are amd64 binaries: set rosetta = true or the provisioning will refuse", tc)
				}
			}
		}
		if b.Cfg.Golden {
			if g, err := b.GoldenName(); err == nil {
				base := "clone of golden " + g
				if _, ok, _ := b.Lima.Get(ctx, g); !ok {
					base += " (built first, once per toolchain set)"
				}
				ui.KV(os.Stderr, "base", base)
			}
		}
		if len(b.Cfg.Hide) > 0 {
			ui.KV(os.Stderr, "hidden in box", strings.Join(b.Cfg.Hide, " "))
		}
		if len(b.Cfg.BoxDirs) > 0 && b.Cfg.Source != config.SourceClone {
			ui.KV(os.Stderr, "on the box disk", strings.Join(b.Cfg.BoxDirs, " ")+ui.Subtle.Render("  (box_dirs: empty on the Mac, fast in the box)"))
		}
		if b.Cfg.Rosetta {
			ui.KV(os.Stderr, "rosetta", "enabled (amd64 binaries / --platform linux/amd64)")
		}
		if b.Cfg.Profile != config.ProfileDefault {
			ui.KV(os.Stderr, "profile", b.Cfg.Profile+" — "+policy.ProfileGuarantee(b.Cfg.Profile))
		} else if b.Cfg.Network == config.NetworkOffline {
			ui.KV(os.Stderr, "network", "offline — egress rejected in the box, sudo removed")
		}
		if b.Cfg.Network == config.NetworkBroker {
			ui.KV(os.Stderr, "egress", fmt.Sprintf("%d allowed host(s) via the broker on %s — `corral egress` lists them", len(box.EgressHosts(b.Cfg)), box.BrokerAddr(b.Name)))
		}
		if b.Cfg.ProtectGitMetadata && !b.Cfg.ReadonlyProject {
			ui.KV(os.Stderr, "git metadata", ".git/config and .git/hooks shadowed in the box")
		}
		fmt.Fprintln(os.Stderr, ui.Subtle.Render("  The first golden on this Mac downloads the Ubuntu image (~600 MB) and provisions toolchains and agents (2–4 min); boxes cloned from it are ready in about 15 seconds."))
		if shouldConfirmCreate(gf.yes, os.Getenv("CORRAL_PLAIN")) && !ui.Confirm(os.Stderr, "Create this box now?", true) {
			return fmt.Errorf("box not created — wrong directory? run corral from the project, or `corral -C <dir> …`; `--yes` skips this question")
		}
	}
	err := ui.RunWithProgress(ctx, "Creating "+b.Name, func(report func(string)) error {
		return b.Create(ctx, report)
	})
	if err != nil {
		return fmt.Errorf("create box: %w\nInspect with `corral logs %s` or start over with `corral delete %s`", err, b.Name, b.Name)
	}
	box.Audit(box.AuditEvent{Event: "create", Box: b.Name, Project: b.Project})
	if a, ok := agent.Lookup(b.Cfg.DefaultAgent); ok {
		ui.Success(os.Stderr, "Box ready. %s", ui.Subtle.Render(a.LoginHint()))
	}
	return nil
}

func printDryRun(b *box.Box, a agent.Agent, spec *box.LaunchSpec) error {
	tpl, hash, err := b.RenderYAML()
	if err != nil {
		return err
	}
	ui.Banner(os.Stdout, Version)
	ui.KV(os.Stdout, "box", b.Name)
	ui.KV(os.Stdout, "project", b.Project)
	ui.KV(os.Stdout, "template hash", hash)
	if a != nil {
		ui.KV(os.Stdout, "agent", a.Name())
	}
	if u := spec.Env["CORRAL_CLONE_URL"]; u != "" {
		ui.KV(os.Stdout, "source", "clone "+u+"@"+spec.Env["CORRAL_CLONE_REF"]+ui.Subtle.Render("  (nothing mounted)"))
	}
	ui.KV(os.Stdout, "network", networkLine(b))
	ui.KV(os.Stdout, "command", strings.Join(spec.Argv, " "))
	ui.KV(os.Stdout, "forwarded env", joinNames(spec.Forwarded)+ui.Subtle.Render("  (names only; values travel over SSH SendEnv)"))
	var static []string
	for k, v := range spec.Env {
		if strings.HasPrefix(k, "CORRAL_") || strings.HasPrefix(k, "GIT_") {
			static = append(static, k+"="+v)
		}
	}
	ui.KV(os.Stdout, "box env", joinNames(static))
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, ui.Header.Render("Lima template that would be used:"))
	fmt.Fprint(os.Stdout, string(tpl))
	return nil
}

// networkLine describes the network mode for info / dry-run.
func networkLine(b *box.Box) string {
	switch b.Cfg.Network {
	case config.NetworkOffline:
		return "offline (egress rejected, sudo removed)"
	case config.NetworkBroker:
		return fmt.Sprintf("broker (egress only to %d allowed host(s) via %s, sudo removed — `corral egress`)", len(box.EgressHosts(b.Cfg)), box.BrokerAddr(b.Name))
	default:
		return "full (outbound internet)"
	}
}

// autoSnapshot takes a copy-on-write snapshot of the box disk before a
// session when snapshot = "auto" and the box is stopped (the common case with
// idle_stop). A running box cannot be snapshotted consistently, so it is
// skipped with a note rather than stopped — booting costs 10–25 s.
func autoSnapshot(ctx context.Context, b *box.Box) {
	if b.Cfg.Snapshot != config.SnapshotAuto {
		return
	}
	_, st, err := b.Status(ctx)
	if err != nil || st != box.StateStopped {
		if st == box.StateRunning && ui.IsTTY() {
			ui.Step(os.Stderr, "snapshot = \"auto\": box already running, no snapshot this session (corral stop first to get one)")
		}
		return
	}
	tag := lima.AutoTag(time.Now())
	if err := b.Lima.SnapshotCreate(ctx, b.Name, tag, nil); err != nil {
		ui.Warning(os.Stderr, "auto snapshot failed: %v", err)
		return
	}
	box.Audit(box.AuditEvent{Event: "snapshot", Box: b.Name, Argv: []string{tag}})
	if removed, err := b.Lima.PruneAutoSnapshots(b.Name, b.Cfg.SnapshotsKeep); err == nil && len(removed) > 0 {
		ui.Step(os.Stderr, "snapshot %s taken · pruned %d older (keep %d) · corral undo rolls back", tag, len(removed), b.Cfg.SnapshotsKeep)
	} else {
		ui.Step(os.Stderr, "snapshot %s taken · corral undo rolls back", tag)
	}
}

func sourceWord(b *box.Box) string {
	switch {
	case b.Cfg.Source == config.SourceClone:
		return "cloned inside the box"
	case b.Cfg.ReadonlyProject:
		return "mounted read-only"
	default:
		return "mounted read/write"
	}
}
