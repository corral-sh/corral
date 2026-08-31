package box

import (
	"strings"
	"testing"

	"github.com/corral-sh/corral/internal/config"
)

func TestNormalizeGitURL(t *testing.T) {
	cases := map[string][2]string{
		"git@git.example.com:group/project.git": {"https://git.example.com/group/project.git", "git.example.com"},
		"https://someone@gitlab.com/g/p.git":    {"https://gitlab.com/g/p.git", "gitlab.com"}, // userinfo dropped
		"http://host.example/g/p":               {"https://host.example/g/p", "host.example"},
		"ssh://git@host.example:2222/g/p.git":   {"https://host.example/g/p.git", "host.example"},
	}
	for in, want := range cases {
		u, h, err := normalizeGitURL(in)
		if err != nil || u != want[0] || h != want[1] {
			t.Errorf("%s → %s %s %v; want %v", in, u, h, err, want)
		}
	}
	for _, bad := range []string{"", "corral", "/local/path", "file:///x"} {
		if _, _, err := normalizeGitURL(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestCloneSpecFromRepoFlag(t *testing.T) {
	cases := map[string][2]string{
		"https://github.com/corral-sh/corral.git@main": {"https://github.com/corral-sh/corral.git", "main"},
		"https://github.com/corral-sh/corral.git":      {"https://github.com/corral-sh/corral.git", ""},
		"https://gitlab.com/g/p.git@v1.2.3":            {"https://gitlab.com/g/p.git", "v1.2.3"},
		"https://gitlab.com/g/p.git":                   {"https://gitlab.com/g/p.git", ""},
	}
	for in, want := range cases {
		b := testBox(t, config.File{})
		b.Repo = in
		cs, err := b.cloneSpec()
		if err != nil || cs.URL != want[0] || cs.Ref != want[1] {
			t.Errorf("%s → %+v %v; want %v", in, cs, err, want)
		}
	}
	for in, want := range map[string]string{"git@h:g/corral.git@main": "/work/corral", "https://h/g/p": "/work/p", "https://h/g/p.git@v1": "/work/p"} {
		b := testBox(t, config.File{})
		b.Repo = in
		if got := b.GuestPath(); got != want {
			t.Errorf("GuestPath(%s) = %s", in, got)
		}
	}
	if b := testBox(t, config.File{}); b.GuestPath() != b.Project {
		t.Error("without --repo the guest path is the project path")
	}
	d := RepoProjectDir("/r", "https://github.com/corral-sh/corral.git@main")
	if d != "/r/github-com-corral-sh-corral" {
		t.Errorf("repo dir %q", d)
	}
}

func TestCloneModeRenderAndLaunch(t *testing.T) {
	clone := config.SourceClone
	b := testBox(t, config.File{Source: &clone, Hide: []string{".env"}, GitTokens: map[string]config.GitToken{"github.com": {Token: "GL"}}})
	b.Repo = "https://github.com/corral-sh/corral.git@main"
	tpl, err := b.Render()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range tpl.Mounts {
		if m.Location == b.Project {
			t.Error("clone mode must not mount the project")
		}
	}
	var dir, shadow, hide bool
	for _, p := range tpl.Provision {
		dir = dir || strings.Contains(p.Script, "install -d -m 0755 /work/corral")
		shadow = shadow || strings.Contains(p.Script, "corral-git-shadow")
		hide = hide || strings.Contains(p.Script, "corral-hide")
	}
	if !dir || shadow || hide {
		t.Errorf("clone provisioning: dir=%v shadow=%v hide=%v", dir, shadow, hide)
	}
	spec, err := b.BuildLaunch(nil, []string{"true"}, true, map[string]string{"GL": "tok"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Env["CORRAL_SOURCE"] != "clone" || spec.Env["CORRAL_CLONE_URL"] != "https://github.com/corral-sh/corral.git" || spec.Env["CORRAL_CLONE_REF"] != "main" {
		t.Errorf("clone env: %+v", spec.Env)
	}
	if spec.GitEnv["CORRAL_GIT_TOKEN_GITHUB_COM"] != "tok" {
		t.Error("token for the clone host must travel")
	}
	if spec.Workdir != "/work/corral" || spec.Env["CORRAL_PROJECT"] != "/work/corral" {
		t.Errorf("--repo workdir: %s %s", spec.Workdir, spec.Env["CORRAL_PROJECT"])
	}
	nb := testBox(t, config.File{Source: &clone})
	nb.Repo = "https://gitlab.com/g/p.git"
	if _, err := nb.BuildLaunch(nil, []string{"true"}, true, nil); err == nil || !strings.Contains(err.Error(), "git_tokens") {
		t.Errorf("missing token must be refused with a git_tokens hint: %v", err)
	}
	m, _ := testBox(t, config.File{}).Render()
	if len(m.Mounts) == 0 || m.Mounts[0].Location == "" {
		t.Error("mount mode still mounts the project")
	}
}
