package cli

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"github.com/corral-sh/corral/internal/box"
	"github.com/corral-sh/corral/internal/lima"
	"github.com/corral-sh/corral/internal/paths"
	"github.com/corral-sh/corral/internal/ui"
)

// golden: list, pre-build and prune the golden images new boxes are cloned from.
func newGoldenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "golden",
		Short:   "Golden images: provisioned once per toolchain set, cloned per project",
		GroupID: "box",
		Long: `A golden image holds the expensive, project-independent part of a box (Ubuntu
base, toolchains, agents). New boxes are copy-on-write clones of it, so the
first start of a project takes a normal boot plus its own packages and scripts
instead of 2–4 minutes. Goldens are keyed by the hash of what they contain: a
new toolchain script version means a new golden, and "prune" removes the old.

  corral golden          list golden images and which boxes use them
  corral golden build    build the golden for this project's config now
  corral golden prune    delete goldens no existing box was cloned from`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			metas, err := box.AllMeta()
			if err != nil {
				return err
			}
			used := map[string]int{}
			var goldens []*box.Meta
			for _, m := range metas {
				if m.IsGolden() {
					goldens = append(goldens, m)
				} else if m.GoldenFrom != "" {
					used[m.GoldenFrom]++
				}
			}
			if len(goldens) == 0 {
				fmt.Println(ui.Subtle.Render("No golden images yet; the first box you build creates one."))
				return nil
			}
			lh, _ := paths.LimaHome()
			lc, _ := lima.New(lh)
			sort.Slice(goldens, func(i, j int) bool { return goldens[i].CreatedAt.After(goldens[j].CreatedAt) })
			var table [][]string
			for _, g := range goldens {
				disk := "-"
				if lc != nil {
					disk = ui.HumanBytes(lima.DiskUsage(lc.InstanceDir(g.Name)))
				}
				table = append(table, []string{g.Name, joinNames(g.Toolchains), g.Disk, disk, fmt.Sprint(used[g.Name]), ui.Ago(g.CreatedAt), g.CorralVersion})
			}
			fmt.Println(ui.Table([]string{"GOLDEN", "TOOLCHAINS", "DISK", "HOST DISK", "BOXES", "BUILT", "CORRAL"}, table))
			return nil
		},
	}
	cmd.AddCommand(newGoldenBuildCmd(), newGoldenPruneCmd())
	return cmd
}

func newGoldenBuildCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "build",
		Short: "Build (or verify) the golden image for this project's configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			b, err := openBox()
			if err != nil {
				return err
			}
			if err := checkHost(ctx, b); err != nil {
				return err
			}
			name, err := b.GoldenName()
			if err != nil {
				return err
			}
			if _, ok, _ := b.Lima.Get(ctx, name); ok {
				ui.Step(os.Stdout, "golden %s already built", name)
				return nil
			}
			if err := ui.RunWithProgress(ctx, "Building golden "+name, func(report func(string)) error {
				_, err := b.EnsureGolden(ctx, report)
				return err
			}); err != nil {
				return err
			}
			pruneOrphanGoldens(ctx)
			return nil
		},
	}
}

// orphanGoldens lists golden images no existing box was cloned from (all:
// every golden), with the host disk each one occupies.
func orphanGoldens(all bool) (names []string, bytes map[string]int64, err error) {
	metas, err := box.AllMeta()
	if err != nil {
		return nil, nil, err
	}
	used := map[string]bool{}
	for _, m := range metas {
		if !m.IsGolden() && m.GoldenFrom != "" {
			used[m.GoldenFrom] = true
		}
	}
	lh, _ := paths.LimaHome()
	lc, _ := lima.New(lh)
	bytes = map[string]int64{}
	for _, m := range metas {
		if m.IsGolden() && (all || !used[m.Name]) {
			names = append(names, m.Name)
			if lc != nil {
				bytes[m.Name] = lima.DiskUsage(lc.InstanceDir(m.Name))
			}
		}
	}
	sort.Strings(names)
	return names, bytes, nil
}

// pruneGoldens deletes the named goldens and reports the disk freed. Shared
// by `golden prune`, `golden build` and `upgrade`: an unattended host
// accumulates one golden per toolchain-script version and nobody is watching
// the disk. Only images no box references are ever passed in.
func pruneGoldens(ctx context.Context, names []string, bytes map[string]int64) error {
	if len(names) == 0 {
		return nil
	}
	lh, err := paths.LimaHome()
	if err != nil {
		return err
	}
	lc, err := lima.New(lh)
	if err != nil {
		return err
	}
	var freed int64
	for _, name := range names {
		if err := ui.RunWithProgress(ctx, "Deleting golden "+name, func(r func(string)) error {
			return box.DeleteGolden(ctx, lc, name, r)
		}); err != nil {
			ui.Warning(os.Stderr, "%v", err)
			continue
		}
		freed += bytes[name]
		box.Audit(box.AuditEvent{Event: "golden-prune", Box: name})
	}
	if freed > 0 {
		ui.Step(os.Stdout, "freed %s of golden images no box was cloned from", ui.HumanBytes(freed))
	}
	return nil
}

// pruneOrphanGoldens is the unattended form: no prompt, never touches a
// golden a box still references. Errors are reported, not returned — pruning
// is housekeeping after the real command.
func pruneOrphanGoldens(ctx context.Context) {
	names, bytes, err := orphanGoldens(false)
	if err != nil || len(names) == 0 {
		return
	}
	if err := pruneGoldens(ctx, names, bytes); err != nil {
		ui.Warning(os.Stderr, "golden prune: %v", err)
	}
}

func newGoldenPruneCmd() *cobra.Command {
	var all, yes, dryRun bool
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Delete golden images no existing box was cloned from (also runs after upgrade and golden build)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			victims, bytes, err := orphanGoldens(all)
			if err != nil {
				return err
			}
			if len(victims) == 0 {
				ui.Step(os.Stdout, "nothing to prune")
				return nil
			}
			var total int64
			for _, v := range victims {
				total += bytes[v]
				fmt.Printf("  %-40s %s\n", v, ui.Subtle.Render(ui.HumanBytes(bytes[v])))
			}
			if dryRun {
				ui.Step(os.Stdout, "would free %s (%d golden image(s)); run without --dry-run to delete", ui.HumanBytes(total), len(victims))
				return nil
			}
			if !yes && !ui.Confirm(os.Stderr, fmt.Sprintf("Delete %d golden image(s), %s? Boxes cloned from them keep working; the next new box rebuilds a golden.", len(victims), ui.HumanBytes(total)), false) {
				return nil
			}
			return pruneGoldens(ctx, victims, bytes)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "list what would be deleted and how much disk it frees")
	cmd.Flags().BoolVar(&all, "all", false, "delete every golden image, used or not")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}
