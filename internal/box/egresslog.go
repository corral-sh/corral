package box

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/corral-sh/corral/internal/paths"
)

// The per-box egress log: ~/.corral/logs/egress-<box>.jsonl, one
// line per connection the box *attempted* through its broker — allowed or
// denied — plus every api_brokers call and a marker at each session start and
// end so the file can be sliced per run. The audit log (sessions.jsonl) keeps
// only denials: it is read whole by the dashboard and must stay small, while
// this file may grow with the box's traffic and is rotated and pruned.
//
// Names only, never payloads: a destination is host:port, an API call is
// method + path + status. The file outlives the box on purpose — "what did
// that run try to reach?" is asked after the box is gone — and `gc` removes
// logs of deleted boxes once they are older than EgressLogRetention.

// EgressRecord is one line of the egress log.
type EgressRecord struct {
	Time time.Time `json:"time"`
	Box  string    `json:"box"`
	Kind string    `json:"kind"` // connect | api | session
	// connect: Host is "host:port"; api: Host is the api_brokers route name.
	Host    string `json:"host,omitempty"`
	Allowed *bool  `json:"allowed,omitempty"` // connect, api
	Method  string `json:"method,omitempty"`  // api
	Path    string `json:"path,omitempty"`    // api
	Status  int    `json:"status,omitempty"`  // api: upstream HTTP status (403 from the broker itself when denied)
	// session: Event is start | end; Agent and Session identify the run,
	// ExitCode is set on end.
	Event    string `json:"event,omitempty"`
	Agent    string `json:"agent,omitempty"`
	Session  string `json:"session,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	PID      int    `json:"pid,omitempty"`
}

const (
	// EgressLogRotateBytes is the size at which the broker rotates the log
	// on start (one previous generation is kept as .1).
	EgressLogRotateBytes = 16 << 20
	// EgressLogRetention is how long the log of a deleted box is kept.
	EgressLogRetention = 14 * 24 * time.Hour
)

// EgressLogPath is the box's egress log file.
func EgressLogPath(name string) (string, error) {
	dir, err := paths.LogsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "egress-"+name+".jsonl"), nil
}

// LogEgress appends one record. Best effort, like Audit: logging must never
// block the broker or the developer. Lines are far below PIPE_BUF, so the
// broker child and the launcher can append concurrently.
func LogEgress(r EgressRecord) {
	if r.Box == "" {
		return
	}
	p, err := EgressLogPath(r.Box)
	if err != nil {
		return
	}
	if r.Time.IsZero() {
		r.Time = time.Now()
	}
	if r.PID == 0 {
		r.PID = os.Getpid()
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	data, err := json.Marshal(r)
	if err != nil {
		return
	}
	_, _ = f.Write(append(data, '\n'))
}

// LogEgressConnect records one destination decision.
func LogEgressConnect(boxName, hostPort string, allowed bool) {
	LogEgress(EgressRecord{Box: boxName, Kind: "connect", Host: hostPort, Allowed: &allowed})
}

// LogEgressAPI records one api_brokers call.
func LogEgressAPI(boxName, api, method, path string, status int, allowed bool) {
	LogEgress(EgressRecord{Box: boxName, Kind: "api", Host: api, Method: method, Path: path, Status: status, Allowed: &allowed})
}

// ReadEgressLog returns the box's records (oldest first) not older than
// since (zero = all). Unparsable lines are skipped. A rotated .1 generation
// is read first so a query across the rotation point stays continuous.
func ReadEgressLog(name string, since time.Time) ([]EgressRecord, error) {
	p, err := EgressLogPath(name)
	if err != nil {
		return nil, err
	}
	var out []EgressRecord
	for _, file := range []string{p + ".1", p} {
		data, err := os.ReadFile(file)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		for _, line := range splitLines(data) {
			var r EgressRecord
			if json.Unmarshal(line, &r) != nil {
				continue
			}
			if !since.IsZero() && r.Time.Before(since) {
				continue
			}
			out = append(out, r)
		}
	}
	return out, nil
}

// RotateEgressLog moves the log aside when it has outgrown
// EgressLogRotateBytes, keeping one previous generation. Called by the broker
// child on start, so a rotation never races an open writer of the same box.
func RotateEgressLog(name string) {
	p, err := EgressLogPath(name)
	if err != nil {
		return
	}
	st, err := os.Stat(p)
	if err != nil || st.Size() < EgressLogRotateBytes {
		return
	}
	_ = os.Rename(p, p+".1")
}

// PruneEgressLogs deletes egress logs (and rotated generations) whose box is
// not in existing and whose last write is older than retention. Returns the
// box names whose logs were removed.
func PruneEgressLogs(existing map[string]bool, retention time.Duration) []string {
	dir, err := paths.LogsDir()
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	cutoff := time.Now().Add(-retention)
	removed := map[string]bool{}
	for _, e := range entries {
		n := e.Name()
		if !strings.HasPrefix(n, "egress-") {
			continue
		}
		boxName := strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(n, "egress-"), ".1"), ".jsonl")
		if boxName == "" || existing[boxName] {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if os.Remove(filepath.Join(dir, n)) == nil {
			removed[boxName] = true
		}
	}
	names := make([]string, 0, len(removed))
	for n := range removed {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// EgressDestination is one host:port (or api route) summarised over a set of
// records: how often it was attempted and with which outcome.
type EgressDestination struct {
	Host    string    `json:"host"`
	Kind    string    `json:"kind"` // connect | api
	Allowed bool      `json:"allowed"`
	Count   int       `json:"count"`
	First   time.Time `json:"first"`
	Last    time.Time `json:"last"`
}

// SummarizeEgress deduplicates connect and api records by destination and
// outcome. Denied entries come first (highest signal), then allowed; each
// group most-attempted first.
func SummarizeEgress(records []EgressRecord) []EgressDestination {
	type key struct {
		host, kind string
		allowed    bool
	}
	idx := map[key]int{}
	var out []EgressDestination
	for _, r := range records {
		if r.Kind != "connect" && r.Kind != "api" {
			continue
		}
		allowed := r.Allowed != nil && *r.Allowed
		k := key{r.Host, r.Kind, allowed}
		i, ok := idx[k]
		if !ok {
			idx[k] = len(out)
			out = append(out, EgressDestination{Host: r.Host, Kind: r.Kind, Allowed: allowed, First: r.Time, Last: r.Time})
			i = len(out) - 1
		}
		out[i].Count++
		if r.Time.Before(out[i].First) {
			out[i].First = r.Time
		}
		if r.Time.After(out[i].Last) {
			out[i].Last = r.Time
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Allowed != out[b].Allowed {
			return !out[a].Allowed
		}
		if out[a].Count != out[b].Count {
			return out[a].Count > out[b].Count
		}
		return out[a].Host < out[b].Host
	})
	return out
}
