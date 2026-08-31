// Package lima is a thin, typed wrapper around the limactl CLI. Corral
// deliberately shells out to limactl instead of linking Lima as a library:
// the CLI is Lima's stable, documented contract, and it keeps the VM driver
// swappable (see internal/box for the only place that renders a template).
package lima

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// MinVersion is the oldest Lima release Corral supports (vz + virtiofs +
// `limactl list --json` shape it relies on).
const MinVersion = "1.0.0"

// TestedVersion is the Lima release Corral is measured and e2e-tested
// against. Lima pins the guest OS image by digest per release, so a different
// Lima can silently mean a different guest; `corral doctor` reports an
// advisory when the installed release is not the tested one.
const (
	TestedVersion = "2.2.0"
	PinnedFormula = "lima"
)

// Tested reports whether a Lima version is the tested minor release.
func Tested(have string) bool {
	h := versionRe.FindStringSubmatch(have)
	w := versionRe.FindStringSubmatch(TestedVersion)
	return h != nil && w != nil && h[1] == w[1] && h[2] == w[2]
}

// Client runs limactl with a fixed LIMA_HOME.
type Client struct {
	Bin      string
	LimaHome string
}

// Instance is the subset of `limactl list --json` Corral uses.
type Instance struct {
	Name         string `json:"name"`
	Status       string `json:"status"`
	Dir          string `json:"dir"`
	VMType       string `json:"vmType"`
	Arch         string `json:"arch"`
	CPUs         int    `json:"cpus"`
	Memory       int64  `json:"memory"`
	Disk         int64  `json:"disk"`
	SSHLocalPort int    `json:"sshLocalPort"`
	SSHConfig    string `json:"sshConfigFile"`
	HostAgentPID int    `json:"hostAgentPID"`
	DriverPID    int    `json:"driverPID"`
	LimaVersion  string `json:"limaVersion"`
	Protected    bool   `json:"protected"`
}

// Running reports whether the instance is up.
func (i Instance) Running() bool { return i.Status == "Running" }

// New locates limactl and returns a client. limaHome may be empty to use
// Lima's default, but Corral always passes its own.
func New(limaHome string) (*Client, error) {
	// CORRAL_LIMACTL > PATH > Homebrew defaults.
	if p := os.Getenv("CORRAL_LIMACTL"); p != "" {
		if _, err := os.Stat(p); err == nil { //nolint:gosec // operator override of which limactl to run; the value is the user's own choice
			return &Client{Bin: p, LimaHome: limaHome}, nil
		}
	}
	bin, err := exec.LookPath("limactl")
	if err != nil {
		for _, p := range []string{"/opt/homebrew/bin/limactl", "/usr/local/bin/limactl"} {
			if _, statErr := os.Stat(p); statErr == nil {
				bin = p
				err = nil
				break
			}
		}
	}
	if err != nil {
		return nil, ErrNotInstalled
	}
	return &Client{Bin: bin, LimaHome: limaHome}, nil
}

// ErrNotInstalled is returned when limactl cannot be found.
var ErrNotInstalled = errors.New("limactl not found — install Lima with `brew install lima`")

func (c *Client) cmd(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	cmd.Env = append(os.Environ(), "LIMA_HOME="+c.LimaHome)
	return cmd
}

var versionRe = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// Version returns the limactl semantic version string, e.g. "2.2.0".
func (c *Client) Version(ctx context.Context) (string, error) {
	out, err := c.cmd(ctx, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("limactl --version: %w", err)
	}
	m := versionRe.FindString(string(out))
	if m == "" {
		return "", fmt.Errorf("unexpected limactl version output %q", strings.TrimSpace(string(out)))
	}
	return m, nil
}

// VersionAtLeast compares dotted versions.
func VersionAtLeast(have, want string) bool {
	h := versionRe.FindStringSubmatch(have)
	w := versionRe.FindStringSubmatch(want)
	if h == nil || w == nil {
		return false
	}
	for i := 1; i <= 3; i++ {
		hi, _ := strconv.Atoi(h[i])
		wi, _ := strconv.Atoi(w[i])
		if hi != wi {
			return hi > wi
		}
	}
	return true
}

// List returns all instances in LIMA_HOME.
func (c *Client) List(ctx context.Context) ([]Instance, error) {
	out, err := c.cmd(ctx, "list", "--json").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, fmt.Errorf("limactl list: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("limactl list: %w", err)
	}
	var list []Instance
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var inst Instance
		if err := json.Unmarshal(line, &inst); err != nil {
			return nil, fmt.Errorf("parse limactl list output: %w", err)
		}
		list = append(list, inst)
	}
	return list, nil
}

// Get returns one instance or ok=false.
func (c *Client) Get(ctx context.Context, name string) (Instance, bool, error) {
	list, err := c.List(ctx)
	if err != nil {
		return Instance{}, false, err
	}
	for _, i := range list {
		if i.Name == name {
			return i, true, nil
		}
	}
	return Instance{}, false, nil
}

// Progress receives limactl log lines while a long operation runs.
type Progress func(line string)

// Create creates and starts a new instance from a template file, streaming
// progress. It blocks until Lima reports READY (all probes satisfied).
func (c *Client) Create(ctx context.Context, name, templatePath string, progress Progress) error {
	return c.stream(ctx, progress, "start", "--tty=false", "--name", name, templatePath)
}

// Clone copies a stopped instance (copy-on-write on APFS, so it takes
// milliseconds) without starting the copy.
func (c *Client) Clone(ctx context.Context, src, dst string, progress Progress) error {
	return c.stream(ctx, progress, "clone", "--tty=false", src, dst)
}

// Start boots an existing, stopped instance.
func (c *Client) Start(ctx context.Context, name string, progress Progress) error {
	return c.stream(ctx, progress, "start", "--tty=false", name)
}

// Stop shuts an instance down gracefully.
func (c *Client) Stop(ctx context.Context, name string, progress Progress) error {
	return c.stream(ctx, progress, "stop", name)
}

// Delete removes an instance and its disks.
func (c *Client) Delete(ctx context.Context, name string, progress Progress) error {
	return c.stream(ctx, progress, "delete", "--force", name)
}

// Snapshot operations (require a stopped instance for vz).
// stream runs limactl, forwarding each stderr/stdout line to progress.
func (c *Client) stream(ctx context.Context, progress Progress, args ...string) error {
	cmd := c.cmd(ctx, args...)
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("limactl %s: %w", args[0], err)
	}
	var last []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := cleanLogLine(sc.Text())
			if line == "" {
				continue
			}
			last = append(last, line)
			if len(last) > 30 {
				last = last[1:]
			}
			if progress != nil {
				progress(line)
			}
		}
	}()
	err := cmd.Wait()
	_ = pw.Close()
	<-done
	if err != nil {
		return &Error{Op: "limactl " + strings.Join(args, " "), Err: err, Tail: last}
	}
	return nil
}

// Error carries the tail of limactl's output for diagnostics.
type Error struct {
	Op   string
	Err  error
	Tail []string
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("%s failed: %v", e.Op, e.Err)
	if len(e.Tail) > 0 {
		msg += "\n" + strings.Join(e.Tail, "\n")
	}
	return msg
}
func (e *Error) Unwrap() error { return e.Err }

var logPrefixRe = regexp.MustCompile(`^time="[^"]*"\s+level=(\w+)\s+msg="(.*)"\s*$`)

// cleanLogLine strips logrus decoration from limactl output.
func cleanLogLine(s string) string {
	s = strings.TrimSpace(s)
	if m := logPrefixRe.FindStringSubmatch(s); m != nil {
		msg := strings.ReplaceAll(m[2], `\"`, `"`)
		msg = strings.ReplaceAll(msg, `\n`, " ")
		if m[1] == "warning" || m[1] == "error" || m[1] == "fatal" {
			return strings.ToUpper(m[1][:1]) + m[1][1:] + ": " + msg
		}
		return msg
	}
	return s
}

// Run executes argv inside the instance non-interactively and returns the
// command's stdout. Used for health probes and version checks.
//
// limactl's own stderr (Lima 2.x prints `level=warning` lines on every
// `shell` call) is never part of the result: a probe that compares the
// output to "active" must not see a warning prepended. On failure the
// error carries both the guest's and limactl's stderr, cleaned.
func (c *Client) Run(ctx context.Context, name string, argv ...string) (string, error) {
	args := append([]string{"shell", "--tty=false", name, "--"}, argv...)
	cmd := c.cmd(ctx, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return string(out), fmt.Errorf("%s: %w\n%s", strings.Join(argv, " "), err, strings.TrimSpace(cleanStderr(stderr.String())))
	}
	return string(out), nil
}

// cleanStderr drops limactl's own log lines (time=… level=… msg=…) so an
// error message shows what the guest command said, not Lima's chatter.
func cleanStderr(s string) string {
	var keep []string
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, "time=") && strings.Contains(l, "level=") {
			continue
		}
		keep = append(keep, l)
	}
	return strings.Join(keep, "\n")
}

// SSHConfigPath returns the per-instance ssh config Lima writes.
func (c *Client) SSHConfigPath(name string) string {
	return filepath.Join(c.LimaHome, name, "ssh.config")
}

// SSHCommand builds an interactive `ssh` invocation into the instance that
// forwards the process environment variables matching sendEnv patterns over
// the encrypted channel (SendEnv). The guest sshd must AcceptEnv them.
// The returned command has no Stdin/Stdout wired; the caller decides.
func (c *Client) SSHCommand(ctx context.Context, name, workdir string, sendEnv []string, argv []string) (*exec.Cmd, error) {
	cfg := c.SSHConfigPath(name)
	if _, err := os.Stat(cfg); err != nil {
		return nil, fmt.Errorf("ssh config for %s not found (is the box running?): %w", name, err)
	}
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		return nil, fmt.Errorf("ssh binary not found: %w", err)
	}
	// ControlMaster is disabled on purpose: Lima keeps a multiplexed master
	// connection whose sshd child read its config before our provisioning
	// added the AcceptEnv rule; a fresh connection always sees current config.
	args := []string{"-F", cfg, "-t", "-q", "-o", "ControlMaster=no", "-o", "ControlPath=none"}
	for _, p := range sendEnv {
		args = append(args, "-o", "SendEnv="+p)
	}
	args = append(args, "lima-"+name, "--", "/opt/corral/bin/corral-launch", shellQuote(workdir))
	for _, a := range argv {
		args = append(args, shellQuote(a))
	}
	return exec.CommandContext(ctx, ssh, args...), nil
}

// InstanceDir returns the on-disk directory of an instance.
func (c *Client) InstanceDir(name string) string { return filepath.Join(c.LimaHome, name) }

// DiskUsage returns the bytes used by an instance directory (sparse-aware).
func DiskUsage(dir string) int64 {
	var total int64
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil //nolint:nilerr // best-effort size: unreadable entries are skipped
		}
		total += allocatedSize(info)
		return nil
	})
	return total
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, " \t\n'\"\\$`!*?[]{}()<>|&;#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
