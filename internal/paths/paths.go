// Package paths centralises every host-side location Corral uses.
//
// Layout (all under $CORRAL_HOME, default ~/.corral):
//
//	config.toml        global user configuration
//	lima/              LIMA_HOME — one sub-directory per box (VM disks, ssh keys)
//	boxes/<name>.json  Corral metadata for a box (project path, agents, template hash)
//	agents/<agent>/    persistent agent state shared by every box (e.g. Claude login)
//	logs/sessions.jsonl audit log of every launch
//
// The directory is deliberately short because Lima places a UNIX socket at
// <LIMA_HOME>/<instance>/ssh.sock.<random> and the whole path must fit in
// UNIX_PATH_MAX (104 bytes on macOS).
package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	envHome = "CORRAL_HOME"
	dirName = ".corral"

	// maxSocketPath is the macOS UNIX_PATH_MAX. Lima appends
	// "/<name>/ssh.sock.<16 chars>" to LIMA_HOME, and we leave headroom.
	maxSocketPath = 104
	socketSuffix  = len("/ssh.sock.1234567890123456")
)

// Home returns the Corral home directory, creating it if needed.
func Home() (string, error) {
	if h := os.Getenv(envHome); h != "" {
		return ensure(h)
	}
	uh, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return ensure(filepath.Join(uh, dirName))
}

// LimaHome is the directory handed to limactl as LIMA_HOME.
func LimaHome() (string, error) {
	h, err := Home()
	if err != nil {
		return "", err
	}
	return ensure(filepath.Join(h, "lima"))
}

// ReposDir holds placeholder project directories for --repo boxes (clone
// mode without a local checkout): identity only, nothing is stored there.
func ReposDir() (string, error) {
	h, err := Home()
	if err != nil {
		return "", err
	}
	return ensure(filepath.Join(h, "repos"))
}

// BoxesDir holds one JSON metadata file per box.
func BoxesDir() (string, error) {
	h, err := Home()
	if err != nil {
		return "", err
	}
	return ensure(filepath.Join(h, "boxes"))
}

// AgentStateDir is the host directory mounted into every box for the given
// agent's persistent state (login, settings, history).
func AgentStateDir(agent string) (string, error) {
	h, err := Home()
	if err != nil {
		return "", err
	}
	return ensure(filepath.Join(h, "agents", agent)) //nolint:gosec // agent is a registry name validated at compile time
}

// LogsDir holds the session audit log.
func LogsDir() (string, error) {
	h, err := Home()
	if err != nil {
		return "", err
	}
	return ensure(filepath.Join(h, "logs"))
}

// GlobalConfigFile is the path of the user-wide TOML config.
func GlobalConfigFile() (string, error) {
	h, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, "config.toml"), nil
}

// ProjectConfigFile is the user-owned per-project config,
// ~/.corral/projects/<box>.toml. It lives outside the repository, so it is
// trusted like the global file: this is where per-project privilege
// (ssh_agent, mounts, git_tokens) belongs.
func ProjectConfigFile(box string) (string, error) {
	h, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, "projects", box+".toml"), nil
}

// MaxBoxNameLen returns how long a box (Lima instance) name may be so that the
// SSH socket path still fits into UNIX_PATH_MAX.
func MaxBoxNameLen() (int, error) {
	lh, err := LimaHome()
	if err != nil {
		return 0, err
	}
	n := maxSocketPath - len(lh) - 1 - socketSuffix
	if n < 8 {
		return 0, fmt.Errorf("CORRAL_HOME %q is too deep: Lima needs the socket path %s/<box>/ssh.sock.* to be shorter than %d bytes. Set CORRAL_HOME to a shorter path", lh, lh, maxSocketPath)
	}
	return n, nil
}

func ensure(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil { //nolint:gosec // every caller passes a path rooted in Home()
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	return dir, nil
}
