package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/corral-sh/corral/internal/box"
	"github.com/corral-sh/corral/internal/config"
	"github.com/corral-sh/corral/internal/lima"
	"github.com/corral-sh/corral/internal/paths"
	"github.com/corral-sh/corral/internal/policy"
	"github.com/corral-sh/corral/internal/ui"
)

func newStopCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:     "stop [box]",
		Short:   "Stop a box (default: the current project's box)",
		GroupID: "box",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if all {
				return forEachRunning(ctx, func(inst lima.Instance, lc *lima.Client) error {
					err := ui.RunWithProgress(ctx, "Stopping "+inst.Name, func(r func(string)) error { return lc.Stop(ctx, inst.Name, r) })
					if err == nil {
						box.StopBrokerFor(inst.Name)
						box.Audit(box.AuditEvent{Event: "stop", Box: inst.Name})
					}
					return err
				})
			}
			b, err := resolveBoxArg(args)
			if err != nil {
				return err
			}
			_, st, err := b.Status(ctx)
			if err != nil {
				return err
			}
			if st != box.StateRunning {
				ui.Step(os.Stdout, "%s is not running", b.Name)
				return nil
			}
			if err := ui.RunWithProgress(ctx, "Stopping "+b.Name, func(r func(string)) error { return b.Lima.Stop(ctx, b.Name, r) }); err != nil {
				return err
			}
			box.StopBroker(b.Meta)
			box.Audit(box.AuditEvent{Event: "stop", Box: b.Name})
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "stop every running box")
	return cmd
}

func newStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "start [box]",
		Short:   "Start a stopped box without launching anything",
		GroupID: "box",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := resolveBoxArg(args)
			if err != nil {
				return err
			}
			return ensureRunning(cmd.Context(), b)
		},
	}
}

func newRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "restart [box]",
		Short:   "Stop and start a box",
		GroupID: "box",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			b, err := resolveBoxArg(args)
			if err != nil {
				return err
			}
			if _, st, err := b.Status(ctx); err != nil {
				return err
			} else if st == box.StateRunning {
				if err := ui.RunWithProgress(ctx, "Stopping "+b.Name, func(r func(string)) error { return b.Lima.Stop(ctx, b.Name, r) }); err != nil {
					return err
				}
				box.StopBroker(b.Meta)
			}
			return ensureRunning(ctx, b)
		},
	}
}

func newRebuildCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "rebuild [box]",
		Short:   "Delete and re-create a box with the current configuration",
		GroupID: "box",
		Long: `Rebuild applies configuration changes (cpus, memory, toolchains, packages,
mounts) — those are fixed when the VM is created. The project directory and
the agent login (in ~/.corral/agents) are untouched; anything installed ad
hoc inside the old box is lost.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			b, err := resolveBoxArg(args)
			if err != nil {
				return err
			}
			if !yes && !ui.Confirm(os.Stderr, fmt.Sprintf("Rebuild %s? Everything installed inside the box since creation is lost.", b.Name), false) {
				return nil
			}
			if err := ui.RunWithProgress(ctx, "Deleting "+b.Name, func(r func(string)) error { return b.Delete(ctx, r) }); err != nil {
				return err
			}
			box.Audit(box.AuditEvent{Event: "delete", Box: b.Name})
			return createBox(ctx, b)
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

func newDeleteCmd() *cobra.Command {
	var yes, all bool
	cmd := &cobra.Command{
		Use:     "delete [box]",
		Aliases: []string{"rm"},
		Short:   "Delete a box (VM disk); the project and agent login are kept",
		GroupID: "box",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if all {
				metas, err := box.AllMeta()
				if err != nil {
					return err
				}
				if len(metas) == 0 {
					ui.Step(os.Stdout, "no boxes")
					return nil
				}
				if !yes && !ui.Confirm(os.Stderr, fmt.Sprintf("Delete all %d boxes?", len(metas)), false) {
					return nil
				}
				for _, m := range metas {
					b, err := openBoxByName(m.Name)
					if err != nil {
						ui.Warning(os.Stderr, "%v", err)
						continue
					}
					if err := ui.RunWithProgress(ctx, "Deleting "+b.Name, func(r func(string)) error { return b.Delete(ctx, r) }); err != nil {
						ui.Warning(os.Stderr, "%v", err)
						continue
					}
					box.Audit(box.AuditEvent{Event: "delete", Box: b.Name})
				}
				return nil
			}
			b, err := resolveBoxArg(args)
			if err != nil {
				return err
			}
			if !yes && !ui.Confirm(os.Stderr, fmt.Sprintf("Delete box %s? The VM disk is removed; %s and your agent login are kept.", b.Name, ui.ShortenHome(b.Project)), false) {
				return nil
			}
			if err := ui.RunWithProgress(ctx, "Deleting "+b.Name, func(r func(string)) error { return b.Delete(ctx, r) }); err != nil {
				return err
			}
			box.Audit(box.AuditEvent{Event: "delete", Box: b.Name})
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	cmd.Flags().BoolVar(&all, "all", false, "delete every box")
	return cmd
}

func newListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls", "status", "ps"},
		Short:   "List boxes and their state (read-only; --json adds live guest metrics for running boxes)",
		GroupID: "insight",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Read-only on purpose: a supervisor polls this; the idle
			// sweep runs on launch, in the dashboard and via `corral gc`.
			rows, err := collectRows(cmd.Context())
			if err != nil {
				return err
			}
			if jsonOut {
				// Live guest metrics are part of the JSON contract:
				// a field that is declared is populated for every running box.
				fillLiveMetrics(cmd.Context(), rows)
				return printJSON(rows)
			}
			if len(rows) == 0 {
				fmt.Println(ui.Subtle.Render("No boxes yet. cd into a project and run `corral claude`."))
				return nil
			}
			var table [][]string
			for _, r := range rows {
				mem := "-"
				if r.Memory > 0 {
					mem = ui.HumanBytes(r.Memory)
					if r.HostMem > 0 { // the Mac's real cost, not the guest's own figure
						mem += ui.Subtle.Render(" · host " + ui.HumanBytes(r.HostMem))
					}
				}
				table = append(table, []string{r.Name, ui.StatusBadge(r.Status), ui.ShortenHome(r.Project), fmt.Sprint(r.CPUs), mem, ui.HumanBytes(r.DiskUsed), ui.Ago(r.LastUsed), ui.DriftBadge(r.Drifted)})
			}
			fmt.Println(ui.Table([]string{"BOX", "STATUS", "PROJECT", "CPU", "MEM", "HOST DISK", "LAST USED", "STATE"}, table))
			if n := goldenCount(); n > 0 {
				fmt.Println(ui.Subtle.Render(fmt.Sprintf("  + %d golden image(s) — corral golden", n)))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output; running boxes carry Load, MemUsed/MemTotal, RootUsed/RootSize, Uptime and HostMem (MetricsErr when a probe fails)")
	return cmd
}

// fillLiveMetrics probes every running box in parallel and fills Load,
// MemUsed/MemTotal, RootUsed/RootSize and Uptime; a failed probe is recorded
// in MetricsErr so zero is never mistaken for a measurement.
func fillLiveMetrics(ctx context.Context, rows []ui.BoxRow) {
	lh, err := paths.LimaHome()
	if err != nil {
		return
	}
	lc, err := lima.New(lh)
	if err != nil {
		return
	}
	var wg sync.WaitGroup
	for i := range rows {
		if rows[i].Status != "Running" {
			continue
		}
		wg.Add(1)
		go func(r *ui.BoxRow) {
			defer wg.Done()
			m, err := probeMetrics(ctx, lc, r.Name)
			if err != nil {
				r.MetricsErr = err.Error()
				return
			}
			r.Load, r.MemUsed, r.MemTotal = m.Load, m.MemUsed, m.MemTotal
			r.RootUsed, r.RootSize, r.Uptime = m.RootUsed, m.RootSize, m.Uptime
		}(&rows[i])
	}
	wg.Wait()
}

// idleSweep stops boxes that have been idle longer than their idle_stop. It
// runs on every launch and in the dashboard (never from `list`, which stays
// read-only), so no daemon is needed; skip is the box the caller is about to
// use. Failures are reported, never fatal.
func idleSweep(ctx context.Context, skip string) {
	lh, err := paths.LimaHome()
	if err != nil {
		return
	}
	lc, err := lima.New(lh)
	if err != nil {
		return
	}
	limit := func(m *box.Meta) time.Duration {
		if cfg, err := loadConfig(m.Project, m.Name); err == nil {
			return cfg.IdleStop
		}
		d, _ := config.Resolve(config.Defaults())
		return d.IdleStop
	}
	stopped, err := box.IdleSweep(ctx, lc, limit, skip)
	for _, s := range stopped {
		fmt.Fprintln(os.Stderr, ui.Subtle.Render(fmt.Sprintf("  stopped %s — idle for %s (idle_stop)", s.Name, s.Idle.Truncate(time.Minute))))
	}
	if err != nil {
		ui.Warning(os.Stderr, "%v", err)
	}
}

func idleStopString(d time.Duration) string {
	if d <= 0 {
		return "off"
	}
	return d.String()
}

// gc is the explicit form of the sweep, for cron or a shell alias.
func newGCCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "gc",
		Short:   "Stop boxes idle longer than their idle_stop (also runs on launch and in the dashboard; `list` never stops anything)",
		GroupID: "box",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			idleSweep(cmd.Context(), "")
			return nil
		},
	}
}

func goldenCount() int {
	metas, _ := box.AllMeta()
	n := 0
	for _, m := range metas {
		if m.IsGolden() {
			n++
		}
	}
	return n
}

// collectRows merges Corral metadata with Lima state.
func collectRows(ctx context.Context) ([]ui.BoxRow, error) {
	lh, err := paths.LimaHome()
	if err != nil {
		return nil, err
	}
	lc, err := lima.New(lh)
	if err != nil {
		return nil, err
	}
	insts, err := lc.List(ctx)
	if err != nil {
		return nil, err
	}
	byName := map[string]lima.Instance{}
	for _, i := range insts {
		byName[i.Name] = i
	}
	metas, err := box.AllMeta()
	if err != nil {
		return nil, err
	}
	footprints := lc.HostFootprints(ctx)
	seen := map[string]bool{}
	var rows []ui.BoxRow
	for _, m := range metas {
		seen[m.Name] = true
		if m.IsGolden() {
			continue // corral golden
		}
		row := ui.BoxRow{Name: m.Name, Project: m.Project, LastUsed: m.LastUsed, Sessions: m.Sessions, Agents: m.Agents, CPUs: m.CPUs}
		if since, ok := m.IdleSince(); ok {
			row.IdleSince = since
		} else {
			row.LiveSessions = len(m.ActiveSessions)
		}
		if inst, ok := byName[m.Name]; ok {
			row.Status = inst.Status
			row.CPUs = inst.CPUs
			row.Memory = inst.Memory
			row.DiskUsed = lima.DiskUsage(inst.Dir)
			row.HostMem = footprints[m.Name]
		}
		if _, statErr := os.Stat(m.Project); statErr == nil {
			if cfg, err := loadConfig(m.Project, m.Name); err == nil {
				cfg.Name = m.Name
				if b, err := box.Open(m.Project, cfg, Version); err == nil {
					row.Drifted, _ = b.Drifted()
				}
				row.Network = cfg.Network
			}
		}
		rows = append(rows, row)
	}
	// Broker boxes: the newest denied destination, so "why did my install
	// fail" is answered from the dashboard.
	if events, err := box.ReadAudit(0); err == nil {
		for i := range rows {
			if rows[i].Network != config.NetworkBroker {
				continue
			}
			for j := len(events) - 1; j >= 0; j-- {
				if events[j].Event == "egress-denied" && events[j].Box == rows[i].Name {
					rows[i].LastDenial, rows[i].LastDenialTime = events[j].Host, events[j].Time
					break
				}
			}
		}
	}
	// Lima instances in our LIMA_HOME without metadata (shouldn't happen, but show them).
	for _, i := range insts {
		if !seen[i.Name] {
			rows = append(rows, ui.BoxRow{Name: i.Name, Status: i.Status, CPUs: i.CPUs, Memory: i.Memory, DiskUsed: lima.DiskUsage(i.Dir), Project: ui.Subtle.Render("(no metadata)")})
		}
	}
	sort.Slice(rows, func(a, b int) bool {
		ra, rb := rows[a].Status == "Running", rows[b].Status == "Running"
		if ra != rb {
			return ra
		}
		return rows[a].LastUsed.After(rows[b].LastUsed)
	})
	return rows, nil
}

func forEachRunning(ctx context.Context, fn func(lima.Instance, *lima.Client) error) error {
	lh, err := paths.LimaHome()
	if err != nil {
		return err
	}
	lc, err := lima.New(lh)
	if err != nil {
		return err
	}
	insts, err := lc.List(ctx)
	if err != nil {
		return err
	}
	n := 0
	for _, i := range insts {
		if i.Running() {
			n++
			if err := fn(i, lc); err != nil {
				ui.Warning(os.Stderr, "%s: %v", i.Name, err)
			}
		}
	}
	if n == 0 {
		ui.Step(os.Stdout, "no running boxes")
	}
	return nil
}

func newInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "info [box]",
		Short:   "Show details of a box: mounts, resources, agents, forwarded env",
		GroupID: "insight",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			b, err := resolveBoxArg(args)
			if err != nil {
				return err
			}
			inst, st, err := b.Status(ctx)
			if err != nil {
				return err
			}
			ui.Banner(os.Stdout, Version)
			status := ""
			if st != box.StateMissing {
				status = inst.Status
			}
			drifted := false
			if b.Meta != nil {
				drifted, _ = b.Drifted()
			}

			ui.Section(os.Stdout, "Box")
			ui.KV(os.Stdout, "box", ui.Bold.Render(b.Name)+"  "+ui.StatusBadge(status)+"  "+ui.DriftBadge(drifted))
			ui.KV(os.Stdout, "project", b.Project)
			ui.KV(os.Stdout, "agents", joinNames(agentNames()))
			ui.KV(os.Stdout, "config files", joinNames(b.Cfg.Sources))
			if b.Cfg.Profile != config.ProfileDefault {
				ui.KV(os.Stdout, "profile", b.Cfg.Profile+ui.Subtle.Render("  ("+policy.ProfileGuarantee(b.Cfg.Profile)+")"))
			}

			ui.Section(os.Stdout, "Boundary")
			mode := "read/write"
			if b.Cfg.ReadonlyProject {
				mode = "read-only"
			}
			if b.Cfg.Source == config.SourceClone {
				ui.KV(os.Stdout, "project mount", "none — source = \"clone\": the repository is cloned inside the box at session start")
			} else {
				ui.KV(os.Stdout, "project mount", mode+ui.Subtle.Render("  (virtiofs, same path inside the box)"))
				git := "shadowed in the box (.git/config, .git/hooks)"
				if !b.Cfg.ProtectGitMetadata || b.Cfg.ReadonlyProject {
					git = ui.Subtle.Render("not shadowed")
				}
				ui.KV(os.Stdout, "git metadata", git)
			}
			ui.KV(os.Stdout, "hidden in box", joinNames(b.Cfg.Hide))
			ui.KV(os.Stdout, "network", networkLine(b))
			ui.KV(os.Stdout, "agent state", agentStateLine(b.Cfg.AgentState))
			var extra []string
			for _, m := range b.Cfg.Mounts {
				extra = append(extra, m.String())
			}
			ui.KV(os.Stdout, "extra mounts", joinNames(extra))
			ui.KV(os.Stdout, "ssh agent", fmt.Sprint(b.Cfg.SSHAgent))
			spec, err := b.BuildLaunch(nil, nil, b.Cfg.Yolo, box.HostEnvMap(os.Environ()))
			if err == nil {
				ui.KV(os.Stdout, "forwarded env", joinNames(spec.Forwarded)+ui.Subtle.Render("  (names only)"))
			}

			ui.Section(os.Stdout, "Resources")
			ui.KV(os.Stdout, "cpu / mem / disk", fmt.Sprintf("%d · %s · %s", b.Cfg.CPUs, b.Cfg.Memory, b.Cfg.Disk))
			ui.KV(os.Stdout, "toolchains", joinNames(b.Cfg.Toolchains))
			ui.KV(os.Stdout, "packages", joinNames(b.Cfg.Packages))
			if b.Cfg.Rosetta {
				ui.KV(os.Stdout, "rosetta", "enabled — amd64 binaries and containers run in the box")
			}
			if st != box.StateMissing {
				ui.KV(os.Stdout, "host disk", ui.HumanBytes(lima.DiskUsage(inst.Dir))+ui.Subtle.Render("  "+inst.Dir))
			}
			if b.Meta != nil && b.Meta.GoldenFrom != "" {
				ui.KV(os.Stdout, "built from", "golden "+b.Meta.GoldenFrom)
			}
			ui.KV(os.Stdout, "snapshot", fmt.Sprintf("%s (keep %d)", b.Cfg.Snapshot, b.Cfg.SnapshotsKeep))

			ui.Section(os.Stdout, "Sessions")
			if b.Meta != nil {
				ui.KV(os.Stdout, "created", b.Meta.CreatedAt.Format("2006-01-02 15:04")+ui.Subtle.Render("  corral "+b.Meta.CorralVersion+" · lima "+b.Meta.LimaVersion))
				ui.KV(os.Stdout, "sessions", fmt.Sprintf("%d · last %s", b.Meta.Sessions, ui.Ago(b.Meta.LastUsed)))
				if since, ok := b.Meta.IdleSince(); ok && st == box.StateRunning {
					ui.KV(os.Stdout, "idle", fmt.Sprintf("since %s · idle_stop %s", ui.Ago(since), idleStopString(b.Cfg.IdleStop)))
				} else if !ok && len(b.Meta.ActiveSessions) > 0 {
					ui.KV(os.Stdout, "idle", fmt.Sprintf("no — %d live session(s)", len(b.Meta.ActiveSessions)))
				}
			}
			if st == box.StateRunning {
				if out, err := b.Lima.Run(ctx, b.Name, "bash", "-lc", "cat /proc/loadavg; uptime -p"); err == nil {
					ui.KV(os.Stdout, "guest", strings.ReplaceAll(strings.TrimSpace(out), "\n", " · "))
				}
			}
			if drifted {
				fmt.Println()
				fmt.Println("  " + ui.Warn.Render("⚠ configuration changed since build — `corral rebuild` to apply"))
			}
			return nil
		},
	}
}

// boxLogFiles returns the Lima logs that exist for b: hostagent stderr and
// the serial console. Shared by `logs` and the dashboard pane so both show
// the same thing.
func boxLogFiles(b *box.Box) ([]string, error) {
	dir := b.Lima.InstanceDir(b.Name)
	files := []string{filepath.Join(dir, "ha.stderr.log"), filepath.Join(dir, "serialv.log"), filepath.Join(dir, "serial.log")}
	var existing []string
	for _, f := range files {
		if _, err := os.Stat(f); err == nil {
			existing = append(existing, f)
		}
	}
	if len(existing) == 0 {
		return nil, fmt.Errorf("no logs for %s yet (%s)", b.Name, dir)
	}
	return existing, nil
}

// tailBoxLogs returns the last n lines of each of the box's log files, with
// the same `==> file <==` headers `tail` prints.
func tailBoxLogs(ctx context.Context, name string, n int) (string, error) {
	b, err := openBoxByName(name)
	if err != nil {
		return "", err
	}
	files, err := boxLogFiles(b)
	if err != nil {
		return "", err
	}
	out, err := exec.CommandContext(ctx, "tail", append([]string{"-n", strconv.Itoa(n)}, files...)...).Output()
	return string(out), err
}

func newLogsCmd() *cobra.Command {
	var follow bool
	cmd := &cobra.Command{
		Use:     "logs [box]",
		Short:   "Show the box's boot/provisioning log (Lima hostagent + serial console)",
		GroupID: "insight",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := resolveBoxArg(args)
			if err != nil {
				return err
			}
			existing, err := boxLogFiles(b)
			if err != nil {
				return err
			}
			tailArgs := []string{"-n", "200"}
			if follow {
				tailArgs = append(tailArgs, "-f")
			}
			c := exec.CommandContext(cmd.Context(), "tail", append(tailArgs, existing...)...)
			c.Stdout, c.Stderr = os.Stdout, os.Stderr
			return c.Run()
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow the logs")
	return cmd
}

func newSnapshotCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "snapshot",
		Short:   "Snapshot a box's disk so you can roll back after an agent session",
		GroupID: "box",
		Long: `Snapshots capture the VM disk (installed tools, caches, the agent's state in
isolated/seeded mode — anything outside the project mount). With source = "mount"
they do NOT include the project directory: use git for that. With source = "clone"
the repository lives on the VM disk, so they cover everything.

A snapshot is an APFS copy-on-write clone of the disk: instant, and it only costs
the blocks that later change. The box must be stopped to snapshot or restore.
snapshot = "auto" takes one at each session start (when the box is stopped) and
keeps the last snapshots_keep; corral undo restores the newest.`,
	}
	withBox := func(use, short string, fn func(ctx context.Context, b *box.Box, tag string) error) *cobra.Command {
		c := &cobra.Command{
			Use:   use,
			Short: short,
			Args:  cobra.RangeArgs(1, 2),
			RunE: func(cmd *cobra.Command, args []string) error {
				var b *box.Box
				var err error
				tag := args[0]
				if len(args) == 2 {
					b, err = openBoxByName(args[0])
					tag = args[1]
				} else {
					b, err = openBox()
				}
				if err != nil {
					return err
				}
				return fn(cmd.Context(), b, tag)
			},
		}
		return c
	}
	stopFirst := func(ctx context.Context, b *box.Box) error {
		_, st, err := b.Status(ctx)
		if err != nil {
			return err
		}
		if st == box.StateRunning {
			return ui.RunWithProgress(ctx, "Stopping "+b.Name+" (snapshots need a stopped box)", func(r func(string)) error { return b.Lima.Stop(ctx, b.Name, r) })
		}
		return nil
	}
	cmd.AddCommand(
		withBox("create [box] <tag>", "Create a snapshot", func(ctx context.Context, b *box.Box, tag string) error {
			if err := stopFirst(ctx, b); err != nil {
				return err
			}
			return ui.RunWithProgress(ctx, "Snapshot "+tag+" of "+b.Name, func(r func(string)) error { return b.Lima.SnapshotCreate(ctx, b.Name, tag, r) })
		}),
		withBox("restore [box] <tag>", "Roll the box disk back to a snapshot", func(ctx context.Context, b *box.Box, tag string) error {
			if err := stopFirst(ctx, b); err != nil {
				return err
			}
			return ui.RunWithProgress(ctx, "Restoring "+b.Name+" to "+tag, func(r func(string)) error { return b.Lima.SnapshotApply(ctx, b.Name, tag, r) })
		}),
		withBox("delete [box] <tag>", "Delete a snapshot", func(ctx context.Context, b *box.Box, tag string) error {
			return ui.RunWithProgress(ctx, "Deleting snapshot "+tag, func(r func(string)) error { return b.Lima.SnapshotDelete(ctx, b.Name, tag, r) })
		}),
		&cobra.Command{
			Use:   "list [box]",
			Short: "List snapshots",
			Args:  cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				b, err := resolveBoxArg(args)
				if err != nil {
					return err
				}
				list, err := b.Lima.SnapshotList(cmd.Context(), b.Name)
				if err != nil {
					return err
				}
				if len(list) == 0 {
					ui.Step(os.Stdout, "no snapshots for %s", b.Name)
					return nil
				}
				var rows [][]string
				for _, sn := range list {
					kind := "manual"
					if strings.HasPrefix(sn.Tag, lima.AutoTagPrefix) {
						kind = "auto"
					}
					rows = append(rows, []string{sn.Tag, kind, sn.Time.Local().Format("2006-01-02 15:04"), ui.HumanBytes(sn.Bytes)})
				}
				fmt.Println(ui.Table([]string{"TAG", "KIND", "TAKEN", "ON DISK"}, rows))
				return nil
			},
		},
	)
	return cmd
}

func agentStateLine(mode string) string {
	switch mode {
	case config.AgentStateSeeded:
		return "seeded — copied from ~/.corral/agents at first boot, then private to this box"
	case config.AgentStateIsolated:
		return "isolated — private to this box (log in once here)"
	default:
		return "shared across boxes (~/.corral/agents, read-write)"
	}
}

func newUndoCmd() *cobra.Command {
	var tag string
	var start bool
	cmd := &cobra.Command{
		Use:     "undo [box]",
		Short:   "Roll the box disk back to the newest automatic snapshot (or --tag)",
		GroupID: "box",
		Long: `Restores the VM disk to the snapshot taken at the start of the last session
(snapshot = "auto"), i.e. "undo what the agent did inside the box". The project
directory is NOT part of it in mount mode — use git there; in clone mode the
repository is on the VM disk and is rolled back too. The box is stopped first.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			b, err := resolveBoxArg(args)
			if err != nil {
				return err
			}
			if tag == "" {
				sn, ok := b.Lima.LatestAutoSnapshot(b.Name)
				if !ok {
					return fmt.Errorf("no automatic snapshot for %s — set snapshot = \"auto\" (or corral snapshot create <tag> before a session), then undo --tag <tag>", b.Name)
				}
				tag = sn.Tag
				ui.Step(os.Stdout, "newest automatic snapshot: %s (%s)", tag, ui.Ago(sn.Time))
			}
			if _, st, err := b.Status(ctx); err != nil {
				return err
			} else if st == box.StateRunning {
				if err := ui.RunWithProgress(ctx, "Stopping "+b.Name, func(r func(string)) error { return b.Lima.Stop(ctx, b.Name, r) }); err != nil {
					return err
				}
				box.StopBroker(b.Meta)
			}
			if err := ui.RunWithProgress(ctx, "Restoring "+b.Name+" to "+tag, func(r func(string)) error { return b.Lima.SnapshotApply(ctx, b.Name, tag, r) }); err != nil {
				return err
			}
			box.Audit(box.AuditEvent{Event: "snapshot-restore", Box: b.Name, Argv: []string{tag}})
			if b.Cfg.Source == config.SourceMount {
				ui.Warning(os.Stderr, "the VM disk is back to %s; %s itself is not covered by snapshots — use git for the project", tag, ui.ShortenHome(b.Project))
			}
			if start {
				return ensureRunning(ctx, b)
			}
			ui.Success(os.Stdout, "%s restored to %s (stopped; next session boots it)", b.Name, tag)
			return nil
		},
	}
	cmd.Flags().StringVar(&tag, "tag", "", "restore this snapshot instead of the newest automatic one")
	cmd.Flags().BoolVar(&start, "start", false, "start the box after restoring")
	return cmd
}
