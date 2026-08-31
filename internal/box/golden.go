package box

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/corral-sh/corral/internal/agent"
	"github.com/corral-sh/corral/internal/lima"
	"github.com/corral-sh/corral/internal/paths"
)

// A golden box is a Lima instance provisioned with only the expensive,
// project-independent parts (base image + apt base, toolchains, agents) and
// then stopped. A project box is an APFS copy-on-write clone of it whose
// lima.yaml is replaced by the project's full template before first start;
// Lima re-runs every provision script on each boot, and ours are idempotent,
// so the clone's first boot costs a normal boot plus the project delta
// (packages, project scripts, shadow units) instead of 2–4 minutes.
//
// Goldens are keyed by the hash of their own template, so a change to a base
// or toolchain script yields a new golden and `corral golden prune`
// removes the unreferenced old one.

// GoldenPrefix names golden instances; they never have a project.
const GoldenPrefix = "golden-"

// GoldenTemplate renders the project-independent template this box would be
// cloned from.
func (b *Box) GoldenTemplate() (*Template, error) { return b.render(true) }

// GoldenName is the golden instance this box clones from: a hash of the golden
// template, so every project with the same toolchains and disk shares one.
func (b *Box) GoldenName() (string, error) {
	t, err := b.GoldenTemplate()
	if err != nil {
		return "", err
	}
	data, err := yaml.Marshal(t)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return GoldenPrefix + hex.EncodeToString(sum[:])[:12], nil
}

// IsGolden reports whether metadata describes a golden image, not a box.
func (m *Meta) IsGolden() bool { return m.Golden }

// EnsureGolden makes sure the golden this box clones from exists and is
// stopped (Lima can only clone a stopped instance). It returns the name.
func (b *Box) EnsureGolden(ctx context.Context, progress lima.Progress) (string, error) {
	name, err := b.GoldenName()
	if err != nil {
		return "", err
	}
	inst, ok, err := b.Lima.Get(ctx, name)
	if err != nil {
		return "", err
	}
	if ok {
		if inst.Running() {
			if err := b.Lima.Stop(ctx, name, progress); err != nil {
				return "", fmt.Errorf("stop golden %s: %w", name, err)
			}
		}
		if _, err := LoadMeta(name); err != nil {
			_ = b.saveGoldenMeta(ctx, name)
		}
		return name, nil
	}
	t, err := b.GoldenTemplate()
	if err != nil {
		return "", err
	}
	data, err := yaml.Marshal(t)
	if err != nil {
		return "", err
	}
	dir, err := paths.BoxesDir()
	if err != nil {
		return "", err
	}
	tpl := filepath.Join(dir, name+".lima.yaml")
	if err := os.WriteFile(tpl, data, 0o600); err != nil {
		return "", err
	}
	if progress != nil {
		progress("building golden image " + name + " (once per toolchain set)")
	}
	if err := b.Lima.Create(ctx, name, tpl, progress); err != nil {
		return "", fmt.Errorf("build golden %s: %w", name, err)
	}
	if err := b.Lima.Stop(ctx, name, progress); err != nil {
		return "", fmt.Errorf("stop golden %s: %w", name, err)
	}
	if err := b.saveGoldenMeta(ctx, name); err != nil {
		return "", err
	}
	Audit(AuditEvent{Event: "golden-build", Box: name})
	return name, nil
}

func (b *Box) saveGoldenMeta(ctx context.Context, name string) error {
	limaVer, _ := b.Lima.Version(ctx)
	now := time.Now()
	return SaveMeta(&Meta{
		Name: name, Golden: true, CreatedAt: now, LastUsed: now,
		Agents: agent.Names(), Toolchains: b.Cfg.Toolchains,
		CPUs: b.Cfg.CPUs, Memory: b.Cfg.Memory, Disk: b.Cfg.Disk,
		CorralVersion: b.Version, LimaVersion: limaVer,
	})
}

// createFromGolden clones the golden, swaps in this box's template and boots.
func (b *Box) createFromGolden(ctx context.Context, progress lima.Progress, data []byte, tpl string) (string, error) {
	golden, err := b.EnsureGolden(ctx, progress)
	if err != nil {
		return "", err
	}
	if progress != nil {
		progress("cloning " + golden + " (copy-on-write)")
	}
	if err := b.Lima.Clone(ctx, golden, b.Name, progress); err != nil {
		return "", err
	}
	// The clone carries the golden's lima.yaml in Lima's *resolved* form: the
	// `base:` template reference has become a concrete `images:` list (and an
	// instance file must not carry `base`). Overlay our template on it — every
	// key we render wins, keys only Lima knows (images, …) survive.
	if err := overlayInstanceYAML(filepath.Join(b.Lima.InstanceDir(b.Name), "lima.yaml"), data); err != nil {
		return "", fmt.Errorf("write template into clone: %w", err)
	}
	_ = tpl
	if err := b.Lima.Start(ctx, b.Name, progress); err != nil {
		return "", err
	}
	return golden, nil
}

// DeleteGolden removes a golden image and its metadata.
func DeleteGolden(ctx context.Context, lc *lima.Client, name string, progress lima.Progress) error {
	if _, ok, err := lc.Get(ctx, name); err != nil {
		return err
	} else if ok {
		if err := lc.Delete(ctx, name, progress); err != nil {
			return err
		}
	}
	if dir, err := paths.BoxesDir(); err == nil {
		_ = os.Remove(filepath.Join(dir, name+".lima.yaml"))
	}
	return DeleteMeta(name)
}

// overlayInstanceYAML merges the rendered template into an existing instance
// lima.yaml (see createFromGolden).
func overlayInstanceYAML(path string, rendered []byte) error {
	existing := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil { //nolint:gosec // path is inside our LIMA_HOME
		if err := yaml.Unmarshal(raw, &existing); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
	}
	ours := map[string]any{}
	if err := yaml.Unmarshal(rendered, &ours); err != nil {
		return err
	}
	delete(ours, "base")
	for k, v := range ours {
		existing[k] = v
	}
	delete(existing, "base")
	out, err := yaml.Marshal(existing)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o600)
}
