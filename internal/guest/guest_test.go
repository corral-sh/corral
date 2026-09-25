package guest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWithProvisionEnvKeepsShebangFirst(t *testing.T) {
	out := WithProvisionEnv("#!/bin/bash\nset -euo pipefail\necho hi\n", map[string]string{"CORRAL_NETWORK": "full"})
	if !strings.HasPrefix(out, "#!/bin/bash\n# --- Corral provision environment") {
		t.Fatalf("shebang must stay on line 1:\n%s", out)
	}
	for _, want := range []string{"export CORRAL_NETWORK=full\n", "{{.User}}", "getent passwd", "export CORRAL_USER CORRAL_HOME\n", "set -euo pipefail\necho hi\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Count(out, "#!") != 1 {
		t.Errorf("exactly one shebang expected:\n%s", out)
	}
	if !strings.HasPrefix(WithProvisionEnv("echo bare\n", nil), "#!/bin/bash\n") {
		t.Error("a script without a shebang gets one")
	}
}

// The header runs on the host's bash: the current user must resolve.
func TestProvisionEnvHeaderResolvesUser(t *testing.T) {
	for _, bin := range []string{"bash", "getent"} { // the guest is Ubuntu; macOS has no getent
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("no %s on this host", bin)
		}
	}
	script := WithProvisionEnv("#!/bin/bash\nset -eu\nprintf '%s|%s|%s' \"$CORRAL_USER\" \"$CORRAL_HOME\" \"$CORRAL_SOURCE\"\n",
		map[string]string{"CORRAL_SOURCE": "mount"})
	// Lima expands {{.User}} when it creates the instance; stand in for it.
	script = strings.ReplaceAll(script, "{{.User}}", currentUser(t))
	cmd := exec.CommandContext(t.Context(), "bash", "-c", script)
	cmd.Env = append(cmd.Environ(), "CORRAL_USER=") // fall back to Lima's user
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	f := strings.Split(string(out), "|")
	if len(f) != 3 || f[0] != currentUser(t) || f[1] == "" || f[2] != "mount" {
		t.Errorf("unexpected header result %q", out)
	}
	bad := exec.CommandContext(t.Context(), "bash", "-c", WithProvisionEnv("#!/bin/bash\necho reached\n", nil))
	bad.Env = append(bad.Environ(), "CORRAL_USER=no-such-user-corral")
	if out, err := bad.CombinedOutput(); err == nil || strings.Contains(string(out), "reached") {
		t.Errorf("an unresolvable user must abort the script, got err=%v out=%s", err, out)
	}
}

func currentUser(t *testing.T) string {
	out, err := exec.CommandContext(t.Context(), "id", "-un").Output()
	if err != nil {
		t.Skip("no id(1)")
	}
	return strings.TrimSpace(string(out))
}

func TestToolchainScriptsPinned(t *testing.T) {
	for _, tc := range []string{"node", "go", "python", "docker", "java", "android", "flutter"} {
		s, ok := ToolchainScript(tc)
		if !ok || !strings.HasPrefix(s, "#!/bin/bash") {
			t.Errorf("toolchain %s missing", tc)
		}
	}
	if _, ok := ToolchainScript("ruby"); ok {
		t.Error("unknown toolchain must not resolve")
	}
	android, _ := ToolchainScript("android")
	for _, want := range []string{"sha256sum -c", "dl.google.com", "binfmt_misc/rosetta", "dpkg --add-architecture amd64", `chown -R "$CORRAL_USER"`} {
		if !strings.Contains(android, want) {
			t.Errorf("android script lacks %q", want)
		}
	}
	flutter, _ := ToolchainScript("flutter")
	for _, want := range []string{"FLUTTER_COMMIT=", "rev-parse HEAD", "refusing", `chown -R "$CORRAL_USER"`} {
		if !strings.Contains(flutter, want) {
			t.Errorf("flutter script lacks %q", want)
		}
	}
	if strings.Contains(android, "| bash") || strings.Contains(flutter, "| bash") {
		t.Error("no curl | bash in toolchains")
	}
}

// The wrapper runs the body unchanged, records a non-zero exit, and stays quiet on success.
func TestRecordedProvisionScript(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	dir := t.TempDir()
	run := func(body string) (string, error) {
		s := strings.ReplaceAll(RecordedProvisionScript("scripts/setup.sh", body), ProvisionFailureDir, dir)
		out, err := exec.CommandContext(t.Context(), "bash", "-c", s).CombinedOutput()
		return string(out), err
	}
	if out, err := run("#!/bin/bash\necho hello\n"); err != nil || !strings.Contains(out, "hello") {
		t.Fatalf("success path: %v %s", err, out)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "*.failed")); len(m) != 0 {
		t.Error("no record on success")
	}
	out, err := run("#!/bin/bash\necho about to fail\nexit 7\n")
	if err == nil {
		t.Fatal("wrapper must propagate the failure")
	}
	m, _ := filepath.Glob(filepath.Join(dir, "*.failed"))
	if len(m) != 1 {
		t.Fatalf("one record expected, got %v (%s)", m, out)
	}
	rec, _ := os.ReadFile(m[0])
	if !strings.Contains(string(rec), "scripts/setup.sh exited 7") {
		t.Errorf("record: %s", rec)
	}
}

// TestDropPrivilegesHelper runs the installed corral-drop-privileges helper
// against stub getent/gpasswd: a group the image lacks (getent exits 2) must not
// fail the lockdown, and a member that stays behind must. The first
// version died on the absent incus-admin group under set -e and every broker
// session refused to start.
func TestDropPrivilegesHelper(t *testing.T) {
	s := Script("drop-privileges")
	start := strings.Index(s, "<<'DROP_EOF'\n")
	end := strings.Index(s, "\nDROP_EOF\n")
	if start < 0 || end < start {
		t.Fatal("helper heredoc not found")
	}
	dir := t.TempDir()
	helper := filepath.Join(dir, "helper.sh")
	body := s[start+len("<<'DROP_EOF'\n") : end+1]
	// The helper's only absolute paths are the sudoers file and the docker socket.
	body = strings.ReplaceAll(body, "/etc/sudoers.d/90-cloud-init-users", filepath.Join(dir, "sudoers"))
	body = strings.ReplaceAll(body, "/run/docker.sock", filepath.Join(dir, "docker.sock"))
	if err := os.WriteFile(helper, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(dir, "bin")
	_ = os.Mkdir(stub, 0o755)
	write := func(name, src string) {
		if err := os.WriteFile(filepath.Join(stub, name), []byte(src), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("getent", "#!/bin/bash\ncase \"$2\" in incus-admin) exit 2;; docker) [ -f \"$STATE/removed\" ] && echo docker:x:111: || echo docker:x:111:alice;; *) echo \"$2:x:1:\";; esac\n")
	write("gpasswd", "#!/bin/bash\n[ -n \"$STICKY\" ] || touch \"$STATE/removed\"\n")
	run := func(sticky bool) (string, error) {
		cmd := exec.CommandContext(t.Context(), "bash", helper)
		cmd.Env = append(os.Environ(), "PATH="+stub+":"+os.Getenv("PATH"), "STATE="+t.TempDir())
		if sticky {
			cmd.Env = append(cmd.Env, "STICKY=1")
		}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run(false); err != nil {
		t.Fatalf("absent group must not fail the lockdown: %v\n%s", err, out)
	}
	if out, err := run(true); err == nil || !strings.Contains(out, "group docker still has members (alice)") {
		t.Fatalf("a member left in docker must fail closed: err=%v\n%s", err, out)
	}
}
