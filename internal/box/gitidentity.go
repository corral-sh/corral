package box

import (
	"context"
	"os/exec"
	"strings"
)

// hostGitIdentity reads user.name / user.email as git resolves them on the
// host (global + includeIf), never touching credential helpers or the rest
// of ~/.gitconfig.
func hostGitIdentity() (name, email string) {
	name = gitConfig("user.name")
	email = gitConfig("user.email")
	return
}

func gitConfig(key string) string {
	out, err := exec.CommandContext(context.Background(), "git", "config", "--get", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
