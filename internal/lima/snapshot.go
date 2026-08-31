package lima

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Snapshots are Corral's own: `limactl snapshot` is unimplemented for the
// vz driver (verified on Lima 2.2: "level=fatal msg=unimplemented"). A vz
// instance's state is two files in its directory — the raw sparse `disk` and
// the `vz-efi` NVRAM — so a snapshot is a copy of both under
// <instance>/corral-snapshots/<tag>/. On APFS the copy is a clonefile:
// instant and copy-on-write, so it costs only the blocks that later diverge.
// The instance must be stopped for create and apply; callers ensure that.

const snapshotsDirName = "corral-snapshots"

var snapshotFiles = []string{"disk", "vz-efi"}

var tagRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// Snapshot is one saved state of an instance disk.
type Snapshot struct {
	Tag   string
	Time  time.Time
	Bytes int64 // allocated size of the copy (shared blocks count once on APFS)
}

// AutoTagPrefix marks snapshots taken automatically at session start.
const AutoTagPrefix = "auto-"

// AutoTag names an automatic snapshot for a point in time.
func AutoTag(t time.Time) string { return AutoTagPrefix + t.UTC().Format("20060102-150405") }

func (c *Client) snapshotDir(name, tag string) string {
	return filepath.Join(c.LimaHome, name, snapshotsDirName, tag)
}

// SnapshotCreate copies the instance's disk state under tag.
func (c *Client) SnapshotCreate(_ context.Context, name, tag string, p Progress) error {
	if !tagRe.MatchString(tag) {
		return fmt.Errorf("snapshot tag %q: letters, digits, . _ - only", tag)
	}
	dst := c.snapshotDir(name, tag)
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("snapshot %q already exists (corral snapshot delete %s %s)", tag, name, tag)
	}
	inst := c.InstanceDir(name)
	if _, err := os.Stat(filepath.Join(inst, "disk")); err != nil {
		return fmt.Errorf("no disk for %s (is the box created?): %w", name, err)
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	for _, f := range snapshotFiles {
		src := filepath.Join(inst, f)
		if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
			continue
		}
		report(p, "cloning "+f)
		if err := cloneFile(src, filepath.Join(dst, f)); err != nil {
			_ = os.RemoveAll(dst)
			return fmt.Errorf("snapshot %s: %w", f, err)
		}
	}
	return nil
}

// SnapshotApply replaces the instance's disk state with the snapshot's. The
// snapshot itself is kept, so applying twice is fine.
func (c *Client) SnapshotApply(_ context.Context, name, tag string, p Progress) error {
	src := c.snapshotDir(name, tag)
	if _, err := os.Stat(filepath.Join(src, "disk")); err != nil {
		return fmt.Errorf("no snapshot %q for %s (corral snapshot list %s)", tag, name, name)
	}
	inst := c.InstanceDir(name)
	for _, f := range snapshotFiles {
		from := filepath.Join(src, f)
		if _, err := os.Stat(from); errors.Is(err, os.ErrNotExist) {
			continue
		}
		report(p, "restoring "+f)
		tmp := filepath.Join(inst, f+".restore")
		if err := cloneFile(from, tmp); err != nil {
			return fmt.Errorf("restore %s: %w", f, err)
		}
		if err := os.Rename(tmp, filepath.Join(inst, f)); err != nil {
			_ = os.Remove(tmp)
			return err
		}
	}
	return nil
}

// SnapshotDelete removes a snapshot.
func (c *Client) SnapshotDelete(_ context.Context, name, tag string, p Progress) error {
	d := c.snapshotDir(name, tag)
	if _, err := os.Stat(d); err != nil {
		return fmt.Errorf("no snapshot %q for %s", tag, name)
	}
	report(p, "deleting "+tag)
	return os.RemoveAll(d)
}

// SnapshotList returns the instance's snapshots, oldest first.
func (c *Client) SnapshotList(_ context.Context, name string) ([]Snapshot, error) {
	entries, err := os.ReadDir(filepath.Join(c.LimaHome, name, snapshotsDirName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Snapshot
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(c.LimaHome, name, snapshotsDirName, e.Name())
		if _, err := os.Stat(filepath.Join(dir, "disk")); err != nil {
			continue
		}
		fi, err := os.Stat(dir) // the directory's mtime is when the snapshot was taken; clones inherit the source file's
		if err != nil {
			continue
		}
		out = append(out, Snapshot{Tag: e.Name(), Time: fi.ModTime(), Bytes: DiskUsage(dir)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out, nil
}

// PruneAutoSnapshots deletes the oldest automatic snapshots beyond keep.
func (c *Client) PruneAutoSnapshots(name string, keep int) ([]string, error) {
	all, err := c.SnapshotList(context.Background(), name)
	if err != nil {
		return nil, err
	}
	var auto []Snapshot
	for _, s := range all {
		if strings.HasPrefix(s.Tag, AutoTagPrefix) {
			auto = append(auto, s)
		}
	}
	var removed []string
	for len(auto) > keep && keep >= 0 {
		if err := os.RemoveAll(c.snapshotDir(name, auto[0].Tag)); err != nil {
			return removed, err
		}
		removed = append(removed, auto[0].Tag)
		auto = auto[1:]
	}
	return removed, nil
}

// LatestAutoSnapshot returns the newest automatic snapshot, if any.
func (c *Client) LatestAutoSnapshot(name string) (Snapshot, bool) {
	all, _ := c.SnapshotList(context.Background(), name)
	for i := len(all) - 1; i >= 0; i-- {
		if strings.HasPrefix(all[i].Tag, AutoTagPrefix) {
			return all[i], true
		}
	}
	return Snapshot{}, false
}

func report(p Progress, msg string) {
	if p != nil {
		p(msg)
	}
}
