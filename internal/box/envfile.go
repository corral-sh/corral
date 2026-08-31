package box

import (
	"bufio"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/corral-sh/corral/internal/config"
)

// LoadEnvFile reads an env_file: KEY=value lines, `#` comments and an
// optional `export ` prefix, single or double quotes stripped. It is the
// credential path for a host with no login session (launchd on a Mac mini),
// so the file is held to the same standard as an SSH private key: a regular
// file, owned by the invoking user, not readable by group or others, inside
// the user's home directory. Anything else refuses — silently running
// without the credential is what an unattended queue would least expect.
func LoadEnvFile(path string) (map[string]string, error) {
	fail := func(why string) error {
		return fmt.Errorf("env_file %s: %s (chmod 0600, own it, keep it under your home directory)", path, why)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(home, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return nil, fail("must be inside your home directory")
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("env_file %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fail("must be a regular file (not a symlink or directory)")
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, fail(fmt.Sprintf("mode %04o is readable by others", fi.Mode().Perm()))
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		if u, err := user.Current(); err == nil {
			if uid, err := strconv.ParseUint(u.Uid, 10, 32); err == nil && uint32(uid) != st.Uid { //nolint:gosec // uid fits
				return nil, fail("owned by another user")
			}
		}
	}
	f, err := os.Open(path) //nolint:gosec // path comes from the trusted config layer and was just checked
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || !config.EnvKeyRe.MatchString(k) {
			return nil, fmt.Errorf("env_file %s:%d: expected KEY=value", path, n)
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// withEnvFile returns hostEnv overlaid with the env_file values for keys the
// host environment does not set (an exported variable always wins), and the
// set of keys the file supplied, for the audit trail. A missing env_file is
// an error: the config asked for it.
func withEnvFile(cfg *config.Config, hostEnv map[string]string) (map[string]string, map[string]bool, error) {
	if cfg.EnvFile == "" {
		return hostEnv, nil, nil
	}
	file, err := LoadEnvFile(cfg.EnvFile)
	if err != nil {
		return nil, nil, err
	}
	merged := make(map[string]string, len(hostEnv)+len(file))
	for k, v := range hostEnv {
		merged[k] = v
	}
	from := map[string]bool{}
	for k, v := range file {
		if hv, ok := merged[k]; ok && hv != "" {
			continue
		}
		merged[k] = v
		from[k] = true
	}
	return merged, from, nil
}
