// Package guest embeds the shell scripts that run inside a box. Everything
// the VM ends up containing is either a digest-pinned Ubuntu cloud image, an
// Ubuntu apt package, or a download whose checksum is verified against the
// vendor's published SHASUMS — no `curl | bash` from third parties except the
// agents' own official installers, which are listed per agent.
package guest

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed scripts/*.sh
var scripts embed.FS

// Script returns the embedded script with the given base name (without .sh).
func Script(name string) string {
	b, err := scripts.ReadFile("scripts/" + name + ".sh")
	if err != nil {
		panic(fmt.Sprintf("guest script %q missing: %v", name, err))
	}
	return string(b)
}

// ToolchainScript returns the installer for a toolchain name from config.
func ToolchainScript(name string) (string, bool) {
	switch name {
	case "node", "go", "python", "docker", "java", "android", "flutter":
		return Script("toolchain-" + name), true
	}
	return "", false
}

// GitShadowScript builds the system-mode script that installs the git
// metadata shadow (see scripts/git-shadow.sh) for the project mounted at
// project. The path is the only per-box input; it is written to
// /etc/corral/git-shadow.conf, which the launcher also uses to know the
// shadow is expected.
func GitShadowScript(project string) string {
	return confScript("git-shadow", map[string]string{"PROJECT": project}) +
		strings.TrimPrefix(Script("git-shadow"), "#!/bin/bash\n")
}

// HideScript builds the system-mode script that installs the hide unit (see
// scripts/hide.sh) for project, with one relative path per line.
func HideScript(project string, hide []string) string {
	return confScript("hide", map[string]string{"PROJECT": project, "HIDE": strings.Join(hide, "\n")}) +
		strings.TrimPrefix(Script("hide"), "#!/bin/bash\n")
}

// BoxDirsScript builds the system-mode script that installs the box_dirs unit
// (see scripts/boxdirs.sh) for project, with one relative directory per line.
func BoxDirsScript(project string, dirs []string) string {
	return confScript("boxdirs", map[string]string{"PROJECT": project, "DIRS": strings.Join(dirs, "\n")}) +
		strings.TrimPrefix(Script("boxdirs"), "#!/bin/bash\n")
}

// ProvisionFailureDir is where a wrapped repository provision script records
// a non-zero exit (one file per script, cleared by base.sh at every boot).
const ProvisionFailureDir = "/corral/runtime/provision"

// RecordedProvisionScript wraps a repository's provision script so that a
// failure is recorded instead of hidden: Lima runs provision scripts through
// cloud-init, which logs a non-zero exit and carries on, and the box then
// reports success with a dependency or control missing. The script body
// runs unchanged in its own bash; the exit status is written to
// ProvisionFailureDir/<key>.failed, which the host checks after every start.
func RecordedProvisionScript(name, script string) string {
	key := shellQuote(strings.NewReplacer("/", "_", " ", "_").Replace(name))
	return "#!/bin/bash\n" +
		"# Corral: repository provision script " + shellQuote(name) + " — exit status recorded, never hidden.\n" +
		"__corral_body=$(mktemp)\n" +
		"cat >\"$__corral_body\" <<'CORRAL_PROVISION_BODY_EOF'\n" + strings.TrimRight(script, "\n") + "\nCORRAL_PROVISION_BODY_EOF\n" +
		"bash \"$__corral_body\"; __corral_rc=$?\n" +
		"rm -f \"$__corral_body\"\n" +
		"if [ \"$__corral_rc\" -ne 0 ]; then\n" +
		"  echo \"provision script " + shellQuote(name) + " exited $__corral_rc\" >" + ProvisionFailureDir + "/" + key + ".failed\n" +
		"  echo \"[corral] provision script " + shellQuote(name) + " FAILED (exit $__corral_rc)\" >&2\n" +
		"  exit \"$__corral_rc\"\n" +
		"fi\n"
}

// confScript writes /etc/corral/<name>.conf as a file the guest units
// `source`: each value is shell-quoted *in the file* (a quoted heredoc carries
// it verbatim), so paths with spaces and multi-line lists survive.
func confScript(name string, vars map[string]string) string {
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sortStrings(keys)
	var sb strings.Builder
	sb.WriteString("#!/bin/bash\nset -euo pipefail\ninstall -d -m 0755 /etc/corral\n")
	fmt.Fprintf(&sb, "cat >/etc/corral/%s.conf <<'CORRAL_CONF_EOF'\n", name)
	for _, k := range keys {
		fmt.Fprintf(&sb, "%s=%s\n", k, shellQuote(vars[k]))
	}
	sb.WriteString("CORRAL_CONF_EOF\n")
	return sb.String()
}

// OfflineScript builds the system-mode script that installs offline mode (see
// scripts/offline.sh).
func OfflineScript() string {
	return confScript("offline", map[string]string{"NETWORK": "offline"}) +
		strings.TrimPrefix(Script("offline"), "#!/bin/bash\n")
}

// SeedStateScript copies the read-only seed of an agent's host state into the
// box's own state directory — once: only while the target is empty, so a
// login refreshed inside the box is never clobbered by a later boot.
func SeedStateScript(seed, state string) string {
	q, t := shellQuote(seed), shellQuote(state)
	return "#!/bin/bash\nset -euo pipefail\nmkdir -p " + t + "\nif [ -d " + q + " ] && [ -z \"$(ls -A " + t + " 2>/dev/null)\" ]; then\n  cp -a " + q + "/. " + t + "/\n  chmod -R u+rwX " + t + "\n  echo \"[corral] seeded agent state from " + seed + "\"\nfi\n"
}

// BrokerScript builds the system-mode script that installs broker mode (see
// scripts/broker.sh): the guest funnel to the allow-list proxy the Mac runs
// on 127.0.0.1:port.
func BrokerScript(port int) string {
	return confScript("broker", map[string]string{"NETWORK": "broker", "PORT": fmt.Sprint(port)}) +
		strings.TrimPrefix(Script("broker"), "#!/bin/bash\n")
}

// ProvisionedMarkerScript is the last user-mode provision step: it marks the
// end of provisioning for units that must wait for it (offline mode).
// The marker holds the kernel boot_id: provision scripts re-run on every
// boot (and a golden clone inherits the previous marker), so only a stamp
// from the current boot means "this boot's provisioning is done".
const ProvisionedMarkerScript = "#!/bin/bash\nset -eu\nmkdir -p \"$HOME/.corral\" && cat /proc/sys/kernel/random/boot_id >\"$HOME/.corral/provisioned\"\n"

// CloneDirScript creates the clone target for source = "clone", owned by the
// box user, so the launcher's git clone can write there.
func CloneDirScript(project string) string {
	q := shellQuote(project)
	return "#!/bin/bash\nset -euo pipefail\nowner=\"\"\nfor h in /home/*; do [ -d \"$h\" ] && owner=$(stat -c '%u:%g' \"$h\") && break; done\n: \"${owner:?no user home}\"\ninstall -d -m 0755 " + q + "\nchown \"$owner\" " + q + "\n"
}

// PackagesScript builds a system-mode script that installs extra apt packages.
func PackagesScript(pkgs []string) string {
	if len(pkgs) == 0 {
		return ""
	}
	quoted := make([]string, len(pkgs))
	for i, p := range pkgs {
		quoted[i] = shellQuote(p)
	}
	return fmt.Sprintf(`#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
echo "[corral] installing project packages: %s"
apt-get install -y --no-install-recommends %s
`, strings.Join(pkgs, " "), strings.Join(quoted, " "))
}

// ProfileScript renders /etc/profile.d/corral.sh with static environment
// that every login shell in the box gets.
func ProfileScript(env map[string]string) string {
	var sb strings.Builder
	sb.WriteString("# Generated by Corral — environment for every shell inside the box.\n")
	sb.WriteString("export CORRAL=1\n")
	sb.WriteString("export PATH=\"/opt/corral/bin:$HOME/.local/bin:$HOME/go/bin:/usr/local/go/bin:$PATH\"\n")
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sortStrings(keys)
	for _, k := range keys {
		fmt.Fprintf(&sb, "export %s=%s\n", k, shellQuote(env[k]))
	}
	return sb.String()
}

// WrapperScript renders a yolo-mode wrapper for an agent binary so that typing
// the bare agent name in `corral shell` behaves the same as the shortcut.
func WrapperScript(binary string, yoloArgs []string) string {
	quoted := make([]string, len(yoloArgs))
	for i, a := range yoloArgs {
		quoted[i] = shellQuote(a)
	}
	return fmt.Sprintf(`#!/bin/bash
# Corral wrapper for %[1]s: adds the agent's "skip prompts" flags when
# CORRAL_YOLO=1 (the box is the safety boundary). Set CORRAL_YOLO=0 or run
# corral with --ask to keep the agent's own permission prompts.
WRAPPER_DIR=/opt/corral/bin
CLEAN_PATH=$(printf '%%s' "$PATH" | tr ':' '\n' | grep -vx "$WRAPPER_DIR" | paste -sd: -)
REAL_BIN=$(PATH="$CLEAN_PATH" command -v %[1]s 2>/dev/null || true)
if [ -z "$REAL_BIN" ]; then
  echo "corral: %[1]s is not installed in this box (run 'corral rebuild')" >&2
  exit 127
fi
if [ "${CORRAL_YOLO:-1}" = "1" ]; then
  exec "$REAL_BIN" %[2]s "$@"
fi
exec "$REAL_BIN" "$@"
`, binary, strings.Join(quoted, " "))
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:+@%", r)
		if !ok {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ShellQuote is exported for callers building guest command lines.
func ShellQuote(s string) string { return shellQuote(s) }

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// WithProvisionEnv prefixes a provision script with the Corral provision
// environment, inserted right after the shebang so `set -e` lines and the
// script's own logic are untouched. Every script — built-in toolchain,
// lockdown unit or a repository's `# corral: system` script — can then
// rely on CORRAL_USER (the box user: Lima gives it the *host's* uid, so
// neither uid 1000 nor SUDO_USER identifies it), CORRAL_HOME, and the
// box's CORRAL_NETWORK / CORRAL_SOURCE.
func WithProvisionEnv(script string, env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sortStrings(keys)
	var sb strings.Builder
	rest := script
	if strings.HasPrefix(script, "#!") {
		if i := strings.IndexByte(script, '\n'); i >= 0 {
			sb.WriteString(script[:i+1])
			rest = script[i+1:]
		} else {
			sb.WriteString(script + "\n")
			rest = ""
		}
	} else {
		sb.WriteString("#!/bin/bash\n")
	}
	sb.WriteString("# --- Corral provision environment (generated; see README \"Provision scripts\") ---\n")
	for _, k := range keys {
		fmt.Fprintf(&sb, "export %s=%s\n", k, shellQuote(env[k]))
	}
	sb.WriteString(provisionUserSnippet)
	sb.WriteString("# --- end of Corral provision environment ---\n")
	sb.WriteString(rest)
	return sb.String()
}

// provisionUserSnippet resolves the box user for a provision script. Lima
// expands {{.User}} in provision scripts at instance creation (its supported
// way to learn the user; referencing LIMA_CIDATA_* draws a warning on every
// `limactl shell`). The fallback is the owner of the single home
// directory on the box disk. Either way the result is verified against
// passwd — a script that chowns to a non-existent user must fail here, not
// three steps later.
const provisionUserSnippet = `if [ -z "${CORRAL_USER:-}" ]; then
  CORRAL_USER="{{.User}}"
  if [ -z "$CORRAL_USER" ]; then
    for __h in /home/*; do [ -d "$__h" ] && CORRAL_USER=$(stat -c %U "$__h") && break; done
  fi
fi
if ! __pw=$(getent passwd "${CORRAL_USER:-}"); then
  echo "corral: cannot resolve the box user (CORRAL_USER='${CORRAL_USER:-}')" >&2
  exit 1
fi
CORRAL_HOME=$(printf '%s' "$__pw" | cut -d: -f6)
export CORRAL_USER CORRAL_HOME
unset __h __pw
`
