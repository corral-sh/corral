package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// ~/.corral/projects/<box>.toml is keyed on the effective box name.
// A box named with --box reads the file of that name; without --box the file
// of the derived name applies, as before.
func TestHostProjectConfigFollowsBoxName(t *testing.T) {
	// Short path on purpose: LIMA_HOME feeds a UNIX socket path capped at 104 bytes.
	home, err := os.MkdirTemp("/tmp", "eb-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("CORRAL_HOME", filepath.Join(home, "eb"))
	project := filepath.Join(home, "proj")
	if err := os.MkdirAll(filepath.Join(home, "eb", "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(home, "eb", "projects", name+".toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("gate-test2", "readonly_project = true\n")
	derived, err := hostProjectConfig(project, "")
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Base(derived[:len(derived)-len(".toml")]), "stop_on_exit = true\n")

	old := gf
	t.Cleanup(func() { gf = old })
	gf = globalFlags{}

	cfg, err := loadConfig(project, "gate-test2")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ReadonlyProject || cfg.StopOnExit || cfg.Name != "gate-test2" {
		t.Errorf("--box gate-test2 must read projects/gate-test2.toml only: readonly=%v stop=%v name=%q", cfg.ReadonlyProject, cfg.StopOnExit, cfg.Name)
	}
	cfg, err = loadConfig(project, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ReadonlyProject || !cfg.StopOnExit {
		t.Errorf("derived name must read its own file: readonly=%v stop=%v", cfg.ReadonlyProject, cfg.StopOnExit)
	}
	// A `name =` in the (trusted) global layer names the host file too.
	if err := os.WriteFile(filepath.Join(home, "eb", "config.toml"), []byte("name = \"gate-test2\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = loadConfig(project, "")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ReadonlyProject {
		t.Error("name = in config must select projects/<name>.toml")
	}
}
