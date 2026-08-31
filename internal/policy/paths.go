package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/corral-sh/corral/internal/config"
	"github.com/corral-sh/corral/internal/paths"
)

// ProjectPath refuses to sandbox directories whose exposure would defeat
// the point (home, root, the Corral state itself).
func ProjectPath(p string) error {
	home, _ := os.UserHomeDir()
	if p == "/" || p == home || p == filepath.Clean(home+"/") {
		return fmt.Errorf("refusing to use %s as the project: it would mount your whole home directory into the box. cd into a project first", p)
	}
	if ebh, err := paths.Home(); err == nil && (p == ebh || strings.HasPrefix(p, ebh+string(os.PathSeparator))) {
		// The one exception: --repo placeholder directories. They give a
		// clone-mode box its identity and are never mounted, so they expose
		// nothing (see box.RepoProjectDir).
		repos := filepath.Join(ebh, "repos") + string(os.PathSeparator)
		if !strings.HasPrefix(p, repos) || strings.Contains(strings.TrimPrefix(p, repos), string(os.PathSeparator)) {
			return fmt.Errorf("refusing to sandbox %s: it is Corral's own state directory", p)
		}
	}
	for _, bad := range []string{"/System", "/usr", "/bin", "/sbin", "/etc", "/private", "/Library"} {
		if p == bad || strings.HasPrefix(p, bad+"/") {
			if !strings.HasPrefix(p, "/private/tmp/") && !strings.HasPrefix(p, "/private/var/folders/") {
				return fmt.Errorf("refusing to sandbox system path %s", p)
			}
		}
	}
	return nil
}

// ProvisionPath resolves a provision entry and confines it to the project.
func ProvisionPath(project, entry string) (string, error) {
	if filepath.IsAbs(entry) {
		return "", fmt.Errorf("provision script %s: must be a path relative to the project root (it is read from the repository, so it may not point at host files)", entry)
	}
	root, err := filepath.EvalSymlinks(project)
	if err != nil {
		return "", fmt.Errorf("provision: resolve project root: %w", err)
	}
	real, err := filepath.EvalSymlinks(filepath.Join(project, entry))
	if err != nil {
		return "", fmt.Errorf("provision script %s: %w", entry, err)
	}
	if real != root && !strings.HasPrefix(real, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("provision script %s resolves to %s, outside the project root; refusing to read it", entry, real)
	}
	fi, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("provision script %s: %w", entry, err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("provision script %s: not a regular file", entry)
	}
	return real, nil
}

// HidePath validates a `hide` entry: a relative path inside the project that
// is not the project itself or its git metadata (the git shadow owns .git).
// It is applied inside the guest, so the host path need not exist yet.
// Returns the cleaned relative path; a trailing slash is preserved as a hint
// that a directory is meant.
func HidePath(entry string) (string, error) {
	if entry == "" || filepath.IsAbs(entry) {
		return "", fmt.Errorf("hide %q: must be a path relative to the project root", entry)
	}
	clean := filepath.Clean(entry)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("hide %q: must stay inside the project (no '..', not the project itself)", entry)
	}
	if clean == ".git" || strings.HasPrefix(clean, ".git/") {
		return "", fmt.Errorf("hide %q: .git is protected by protect_git_metadata, not hide", entry)
	}
	if strings.HasSuffix(entry, "/") {
		clean += "/"
	}
	return clean, nil
}

// BoxDirPath validates one `box_dirs` entry: a directory inside the project
// that the box keeps on its own disk (bind-mounted over the virtiofs mount).
// Same confinement as hide — relative, inside the project, never the project
// itself and never .git (the shadow owns that).
func BoxDirPath(entry string) (string, error) {
	if entry == "" || filepath.IsAbs(entry) {
		return "", fmt.Errorf("box_dirs %q: must be a path relative to the project root", entry)
	}
	clean := filepath.Clean(entry)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("box_dirs %q: must stay inside the project (no '..', not the project itself)", entry)
	}
	if clean == ".git" || strings.HasPrefix(clean, ".git/") {
		return "", fmt.Errorf("box_dirs %q: .git cannot live on the box disk", entry)
	}
	return clean, nil
}

// ExtraMount refuses extra mounts whose exposure would defeat the sandbox.
// Add to the list rather than loosening it.
func ExtraMount(m config.Mount) error {
	home, _ := os.UserHomeDir()
	if m.Host == home {
		return fmt.Errorf("mount %s: refusing to mount your entire home directory", m.Host)
	}
	for _, sensitive := range []string{".ssh", ".aws", ".gnupg", ".config/gh", ".kube", ".docker", ".claude", ".zshenv", ".zshrc", ".bash_profile", ".netrc"} {
		p := filepath.Join(home, sensitive)
		if m.Host == p || strings.HasPrefix(p, m.Host+string(os.PathSeparator)) {
			return fmt.Errorf("mount %s would expose %s to the box; forward a scoped token with env_from_host instead", m.Host, p)
		}
	}
	if _, err := os.Stat(m.Host); err != nil {
		return fmt.Errorf("mount %s: %w", m.Host, err)
	}
	return nil
}
