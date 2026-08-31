package box

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/corral-sh/corral/internal/paths"
)

// AuditEvent is one line in ~/.corral/logs/sessions.jsonl. It never
// contains secret values — only the names of forwarded variables.
type AuditEvent struct {
	Time      time.Time `json:"time"`
	Event     string    `json:"event"` // launch | exit | refused | create | delete | stop | idle-stop | broker-start | broker-stop | egress-denied
	Box       string    `json:"box"`
	Project   string    `json:"project,omitempty"`
	Agent     string    `json:"agent,omitempty"`
	Argv      []string  `json:"argv,omitempty"`
	Yolo      *bool     `json:"yolo,omitempty"`
	Forwarded []string  `json:"forwarded_env,omitempty"`
	ExitCode  *int      `json:"exit_code,omitempty"`
	Outcome   string    `json:"outcome,omitempty"` // exit | timeout | unreachable | preflight-refused | admission-refused
	Duration  string    `json:"duration,omitempty"`
	User      string    `json:"user,omitempty"`
	Host      string    `json:"host,omitempty"`   // egress-denied: destination name; api-call/api-denied: the api_brokers name (never a payload)
	Status    int       `json:"status,omitempty"` // api-call: upstream HTTP status
	// Who did it: the corral subcommand (never its arguments, which may be
	// a user's command line) and the process that invoked corral. Added
	// because "boxes vanished" was unattributable with only the event.
	Cmd    string `json:"cmd,omitempty"`
	PID    int    `json:"pid,omitempty"`
	Parent string `json:"parent,omitempty"` // "<ppid> <comm>", e.g. "8123 zsh" or "2 bash" (a script)
}

// Audit appends an event to the session log. Failures are ignored: auditing
// must never block the developer.
func Audit(ev AuditEvent) {
	dir, err := paths.LogsDir()
	if err != nil {
		return
	}
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	if ev.User == "" {
		ev.User = os.Getenv("USER")
	}
	if ev.Cmd == "" {
		ev.Cmd = invokedSubcommand()
	}
	if ev.PID == 0 {
		ev.PID = os.Getpid()
		ev.Parent = parentProcess()
	}
	f, err := os.OpenFile(filepath.Join(dir, "sessions.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	_, _ = f.Write(append(data, '\n'))
}

// ReadAudit returns the last n events (newest last).
func ReadAudit(n int) ([]AuditEvent, error) {
	dir, err := paths.LogsDir()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "sessions.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var events []AuditEvent
	for _, line := range splitLines(data) {
		var ev AuditEvent
		if json.Unmarshal(line, &ev) == nil {
			events = append(events, ev)
		}
	}
	if n > 0 && len(events) > n {
		events = events[len(events)-n:]
	}
	return events, nil
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			if i > start {
				out = append(out, b[start:i])
			}
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}

// invokedSubcommand is the corral subcommand path of this process
// ("delete", "snapshot create", "claude"): the first two non-flag arguments,
// stopping at "--". Arguments after the subcommand are a user's own command
// line and never recorded.
func invokedSubcommand() string {
	// Only these parents have a sub-verb worth recording; anything after any
	// other command is the user's own input (a prompt, a shell line).
	groups := map[string]bool{"snapshot": true, "golden": true, "config": true, "agents": true}
	var parts []string
	skipNext := false
	for _, a := range os.Args[1:] {
		if a == "--" || len(parts) == 2 || (len(parts) == 1 && !groups[parts[0]]) {
			break
		}
		if skipNext {
			skipNext = false
			continue
		}
		if strings.HasPrefix(a, "-") {
			// Value-taking global flags: -C <dir>, --box <name>, --repo <url>, --project <dir>.
			if a == "-C" || a == "--box" || a == "--repo" || a == "--project" || a == "--editor" || a == "--tag" || a == "--profile" {
				skipNext = true
			}
			continue
		}
		parts = append(parts, a)
	}
	if len(parts) == 0 {
		return "dashboard" // bare corral
	}
	return strings.Join(parts, " ")
}

// parentProcess returns "<ppid> <command name>" of the process that started
// corral — a shell (a human), a script, or corral itself (a broker child).
func parentProcess() string {
	ppid := os.Getppid()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "comm=", "-p", strconv.Itoa(ppid)).Output()
	name := strings.TrimSpace(string(out))
	if err != nil || name == "" {
		return strconv.Itoa(ppid)
	}
	return strconv.Itoa(ppid) + " " + filepath.Base(name)
}
