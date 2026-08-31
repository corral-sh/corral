package paths

import "path/filepath"

// SSHIncludeFile is the ssh_config fragment Corral maintains for editors:
// one Include per box pointing at the ssh.config Lima regenerates on every
// start, so `ssh lima-<box>` (and VS Code / JetBrains over it) always sees the
// current port. Users opt in by including it from ~/.ssh/config.
func SSHIncludeFile() (string, error) {
	h, err := Home()
	if err != nil {
		return "", err
	}
	d, err := ensure(filepath.Join(h, "ssh"))
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config"), nil
}
