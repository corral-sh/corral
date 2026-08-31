package box

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
)

// Source modes: how the project's code reaches the box.
//
//   - mount (default): the host directory is a live virtiofs mount — edits are
//     on the Mac immediately, and so is anything the agent writes.
//   - clone: nothing from the host is mounted. At session start the launcher
//     clones the repository *inside* the box, at the same path the project has
//     on the host, using the trusted git_tokens credential; the handoff back is
//     a pushed branch. There is nothing a hostile checkout can leave for the
//     Mac to run, at the cost of live editing and uncommitted work.
//
// The URL and ref are per-session environment, not template fields, so a
// branch switch on the host never marks the box as drifted.

// CloneSpec is what the launcher needs to clone inside the box.
type CloneSpec struct {
	URL  string // https URL the guest clones (token via the credential helper)
	Host string // hostname the token is looked up for
	Ref  string // branch, tag or commit to check out; "" = remote default
}

// cloneSpec resolves the repository for clone mode: an explicit --repo
// override, or the local checkout's origin and current branch.
func (b *Box) cloneSpec() (CloneSpec, error) {
	raw, ref := b.Repo, ""
	if i := strings.LastIndex(raw, "@"); i > 0 && !strings.Contains(raw[i:], "/") && !strings.HasPrefix(raw, "git@") || (strings.HasPrefix(raw, "git@") && strings.Count(raw, "@") > 1) {
		raw, ref = raw[:i], raw[i+1:]
	}
	if raw == "" {
		origin, branch, err := hostGitOrigin(b.Project)
		if err != nil {
			return CloneSpec{}, err
		}
		raw, ref = origin, branch
	}
	https, host, err := normalizeGitURL(raw)
	if err != nil {
		return CloneSpec{}, err
	}
	return CloneSpec{URL: https, Host: host, Ref: ref}, nil
}

// hostGitOrigin reads origin and the current branch of the checkout on the
// host (read-only git queries, no credential helpers involved).
func hostGitOrigin(project string) (origin, branch string, err error) {
	git := func(args ...string) (string, error) {
		out, err := exec.CommandContext(context.Background(), "git", append([]string{"-C", project}, args...)...).Output()
		return strings.TrimSpace(string(out)), err
	}
	origin, err = git("remote", "get-url", "origin")
	if err != nil || origin == "" {
		return "", "", fmt.Errorf("source = \"clone\": %s has no git remote \"origin\"; pass --repo <url>[@ref] or use source = \"mount\"", project)
	}
	branch, _ = git("rev-parse", "--abbrev-ref", "HEAD")
	if branch == "HEAD" { // detached
		branch, _ = git("rev-parse", "HEAD")
	}
	return origin, branch, nil
}

var scpLikeRe = regexp.MustCompile(`^(?:[A-Za-z0-9_.-]+@)?([A-Za-z0-9_.-]+):([^/].*)$`)

// normalizeGitURL turns any common git remote form into the https URL the box
// clones over (the box has no SSH keys unless ssh_agent is on) and the host
// the token must exist for.
func normalizeGitURL(raw string) (https, host string, err error) {
	switch {
	case strings.HasPrefix(raw, "https://"), strings.HasPrefix(raw, "http://"):
		u, perr := url.Parse(raw)
		if perr != nil || u.Host == "" {
			return "", "", fmt.Errorf("repository URL %q: %w", raw, perr)
		}
		u.User = nil
		u.Scheme = "https"
		return u.String(), u.Hostname(), nil
	case strings.HasPrefix(raw, "ssh://"):
		u, perr := url.Parse(raw)
		if perr != nil || u.Host == "" {
			return "", "", fmt.Errorf("repository URL %q: %w", raw, perr)
		}
		return "https://" + u.Hostname() + "/" + strings.TrimPrefix(u.Path, "/"), u.Hostname(), nil
	}
	if m := scpLikeRe.FindStringSubmatch(raw); m != nil {
		return "https://" + m[1] + "/" + m[2], m[1], nil
	}
	return "", "", fmt.Errorf("repository URL %q: expected https://host/group/repo.git or git@host:group/repo.git", raw)
}

// GuestPath is where the project lives inside the box: the host path in
// mount mode and for clone mode from a checkout (agents key settings by
// path), or /work/<repo> for a --repo box that has no checkout.
func (b *Box) GuestPath() string {
	if b.Repo == "" {
		return b.Project
	}
	raw := b.Repo
	if i := strings.LastIndex(raw, "@"); i > 0 && !strings.HasPrefix(raw, "git@") || strings.Count(raw, "@") > 1 {
		raw = raw[:i]
	}
	raw = strings.TrimSuffix(strings.TrimRight(raw, "/"), ".git")
	name := raw[strings.LastIndexAny(raw, "/:")+1:]
	if name == "" {
		name = "repo"
	}
	return "/work/" + name
}

// RepoProjectDir is the host-side placeholder directory that gives a --repo
// box its identity (name, host-project config) when there is no checkout.
func RepoProjectDir(reposDir, repo string) string {
	raw := repo
	if i := strings.LastIndex(raw, "@"); i > 0 && !strings.HasPrefix(raw, "git@") || strings.Count(raw, "@") > 1 {
		raw = raw[:i]
	}
	if https, _, err := normalizeGitURL(raw); err == nil {
		raw = https
	}
	raw = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(raw, "https://"), "http://"), ".git")
	slug := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(raw), "-"), "-")
	if len(slug) > 60 {
		slug = slug[len(slug)-60:]
	}
	return reposDir + "/" + slug
}
