package lima

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSnapshotLifecycle(t *testing.T) {
	home := t.TempDir()
	c := &Client{LimaHome: home}
	inst := filepath.Join(home, "b1")
	_ = os.MkdirAll(inst, 0o700)
	write := func(f, s string) {
		if err := os.WriteFile(filepath.Join(inst, f), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("disk", "v1")
	write("vz-efi", "nvram1")

	if err := c.SnapshotCreate(context.Background(), "b1", "bad tag!", nil); err == nil {
		t.Fatal("bad tag accepted")
	}
	if err := c.SnapshotCreate(context.Background(), "b1", "one", nil); err != nil {
		t.Fatal(err)
	}
	if err := c.SnapshotCreate(context.Background(), "b1", "one", nil); err == nil {
		t.Fatal("duplicate tag accepted")
	}
	write("disk", "v2")
	write("vz-efi", "nvram2")
	if err := c.SnapshotApply(context.Background(), "b1", "one", nil); err != nil {
		t.Fatal(err)
	}
	d, _ := os.ReadFile(filepath.Join(inst, "disk"))
	e, _ := os.ReadFile(filepath.Join(inst, "vz-efi"))
	if string(d) != "v1" || string(e) != "nvram1" {
		t.Fatalf("restore gave disk=%q efi=%q", d, e)
	}
	// The snapshot survives an apply and is independent of later writes.
	write("disk", "v3")
	if err := c.SnapshotApply(context.Background(), "b1", "one", nil); err != nil {
		t.Fatal(err)
	}
	d, _ = os.ReadFile(filepath.Join(inst, "disk"))
	if string(d) != "v1" {
		t.Fatalf("second apply gave %q", d)
	}
	list, err := c.SnapshotList(context.Background(), "b1")
	if err != nil || len(list) != 1 || list[0].Tag != "one" || list[0].Bytes == 0 {
		t.Fatalf("list %+v %v", list, err)
	}
	if err := c.SnapshotDelete(context.Background(), "b1", "one", nil); err != nil {
		t.Fatal(err)
	}
	if err := c.SnapshotDelete(context.Background(), "b1", "one", nil); err == nil {
		t.Fatal("deleting a missing snapshot must fail")
	}
	if err := c.SnapshotApply(context.Background(), "b1", "missing", nil); err == nil {
		t.Fatal("applying a missing snapshot must fail")
	}
}

func TestAutoSnapshotPrune(t *testing.T) {
	home := t.TempDir()
	c := &Client{LimaHome: home}
	inst := filepath.Join(home, "b1")
	_ = os.MkdirAll(inst, 0o700)
	_ = os.WriteFile(filepath.Join(inst, "disk"), []byte("x"), 0o600)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		tag := AutoTag(base.Add(time.Duration(i) * time.Minute))
		if err := c.SnapshotCreate(context.Background(), "b1", tag, nil); err != nil {
			t.Fatal(err)
		}
		// Make mtimes distinct and ordered.
		_ = os.Chtimes(filepath.Join(inst, snapshotsDirName, tag), base.Add(time.Duration(i)*time.Minute), base.Add(time.Duration(i)*time.Minute))
	}
	if err := c.SnapshotCreate(context.Background(), "b1", "manual", nil); err != nil {
		t.Fatal(err)
	}
	removed, err := c.PruneAutoSnapshots("b1", 2)
	if err != nil || len(removed) != 2 {
		t.Fatalf("removed %v err %v", removed, err)
	}
	list, _ := c.SnapshotList(context.Background(), "b1")
	if len(list) != 3 { // 2 auto + manual
		t.Fatalf("after prune: %+v", list)
	}
	latest, ok := c.LatestAutoSnapshot("b1")
	if !ok || latest.Tag != AutoTag(base.Add(3*time.Minute)) {
		t.Fatalf("latest %+v", latest)
	}
}
