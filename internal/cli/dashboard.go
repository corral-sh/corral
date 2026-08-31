package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/corral-sh/corral/internal/agent"
	"github.com/corral-sh/corral/internal/box"
	"github.com/corral-sh/corral/internal/lima"
	"github.com/corral-sh/corral/internal/paths"
	"github.com/corral-sh/corral/internal/policy"
	"github.com/corral-sh/corral/internal/ui"
)

// runDashboard shows the TUI and executes the action the user picked.
func runDashboard(ctx context.Context) error {
	if !ui.IsTTY() {
		return fmt.Errorf("the dashboard needs an interactive terminal; try `corral list`")
	}
	gp, _ := paths.GlobalConfigFile()
	cfg, err := policy.Load(gp, "", "")
	if err != nil {
		return err
	}
	// First run: no boxes and no config → guide the user.
	metas, _ := box.AllMeta()
	if len(metas) == 0 {
		if _, err := os.Stat(gp); err != nil {
			ui.Banner(os.Stdout, Version)
			fmt.Println()
			fmt.Println(ui.Subtle.Render("  Welcome! Nothing is set up yet."))
			fmt.Printf("  %s %s   configure defaults and check prerequisites\n", ui.Info.Render("→"), ui.Code.Render("corral setup"))
			fmt.Printf("  %s %s   from inside a project — builds the box and starts Claude\n", ui.Info.Render("→"), ui.Code.Render("corral claude"))
			fmt.Printf("  %s %s   check the host\n", ui.Info.Render("→"), ui.Code.Render("corral doctor"))
			return nil //nolint:nilerr // no config yet is the welcome case, not an error
		}
	}

	lh, err := paths.LimaHome()
	if err != nil {
		return err
	}
	lc, err := lima.New(lh)
	if err != nil {
		return err
	}
	src := ui.DashboardSource{
		Version:      Version,
		DefaultAgent: cfg.DefaultAgent,
		Rows: func(ctx context.Context) ([]ui.BoxRow, error) {
			idleSweep(ctx, "")
			return collectRows(ctx)
		},
		Metrics: func(ctx context.Context, name string) (ui.BoxRow, error) {
			row, err := probeMetrics(ctx, lc, name)
			if err != nil {
				return row, err
			}
			row.HostMem = lc.HostFootprints(ctx)[name]
			return row, nil
		},
		Toggle: func(ctx context.Context, row ui.BoxRow, report func(string)) error {
			if row.Status == "Running" {
				defer box.StopBrokerFor(row.Name)
				if err := lc.Stop(ctx, row.Name, report); err != nil {
					return err
				}
				box.Audit(box.AuditEvent{Event: "stop", Box: row.Name})
				return nil
			}
			return lc.Start(ctx, row.Name, report)
		},
		Logs: func(ctx context.Context, row ui.BoxRow) (string, error) {
			return tailBoxLogs(ctx, row.Name, 500)
		},
		Delete: func(ctx context.Context, row ui.BoxRow, report func(string)) error {
			b, err := openBoxByName(row.Name)
			if err != nil {
				// No metadata: delete the raw Lima instance.
				if err := lc.Delete(ctx, row.Name, report); err != nil {
					return err
				}
				return box.DeleteMeta(row.Name)
			}
			if err := b.Delete(ctx, report); err != nil {
				return err
			}
			box.Audit(box.AuditEvent{Event: "delete", Box: row.Name})
			return nil
		},
	}
	action, err := ui.RunDashboard(ctx, src)
	if err != nil {
		return err
	}
	switch action.Kind {
	case "launch", "shell":
		if _, err := os.Stat(action.Box.Project); err != nil {
			return fmt.Errorf("project %s no longer exists", action.Box.Project)
		}
		gf.project = action.Box.Project
		gf.name = action.Box.Name
		if action.Kind == "shell" {
			return launch(ctx, nil, []string{"bash", "-l"}, &launchFlags{})
		}
		a, ok := agent.Lookup(cfg.DefaultAgent)
		if !ok {
			return launch(ctx, nil, []string{"bash", "-l"}, &launchFlags{})
		}
		return launch(ctx, a, nil, &launchFlags{})
	}
	return nil
}

// probeMetrics reads the guest's load, memory, root disk and uptime in one
// `limactl shell`. Shared by the dashboard and `list --json`.
func probeMetrics(ctx context.Context, lc *lima.Client, name string) (ui.BoxRow, error) {
	out, err := lc.Run(ctx, name, "bash", "-c",
		`cat /proc/loadavg | cut -d' ' -f1-3; grep -E '^(MemTotal|MemAvailable):' /proc/meminfo | awk '{print $2}'; df -B1 --output=size,used / | tail -1; uptime -p`)
	if err != nil {
		return ui.BoxRow{Name: name}, err
	}
	return parseMetrics(name, out), nil
}

func parseMetrics(name, out string) ui.BoxRow {
	row := ui.BoxRow{Name: name}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) >= 1 {
		row.Load = strings.TrimSpace(lines[0])
	}
	if len(lines) >= 3 {
		total, _ := strconv.ParseInt(strings.TrimSpace(lines[1]), 10, 64)
		avail, _ := strconv.ParseInt(strings.TrimSpace(lines[2]), 10, 64)
		row.MemTotal = total * 1024
		row.MemUsed = (total - avail) * 1024
	}
	if len(lines) >= 4 {
		f := strings.Fields(lines[3])
		if len(f) >= 2 {
			row.RootSize, _ = strconv.ParseInt(f[0], 10, 64)
			row.RootUsed, _ = strconv.ParseInt(f[1], 10, 64)
		}
	}
	if len(lines) >= 5 {
		row.Uptime = strings.TrimPrefix(strings.TrimSpace(lines[4]), "up ")
	}
	return row
}
