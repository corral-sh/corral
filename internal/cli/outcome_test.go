package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Admission: a count ceiling and a measured-footprint memory budget.
func TestAdmission(t *testing.T) {
	gib := uint64(1 << 30)
	cases := []struct {
		name                      string
		running, maxRunning       int
		used, want, host, reserve uint64
		ok                        bool
	}{
		{"no limits, memory unknown", 5, 0, 0, 4 * gib, 0, 8 * gib, true},
		{"count reached", 2, 2, 0, 4 * gib, 64 * gib, 8 * gib, false},
		{"count below", 1, 2, 0, 4 * gib, 64 * gib, 8 * gib, true},
		{"budget fits: 12.4 used + 12 wanted < 32-8", 3, 0, 12400 << 20, 12 * gib, 32 * gib, 8 * gib, false},
		{"budget fits: 6 used + 4 wanted < 24", 2, 0, 6 * gib, 4 * gib, 32 * gib, 8 * gib, true},
		{"reserve larger than host", 0, 0, 0, gib, 4 * gib, 8 * gib, false},
	}
	for _, c := range cases {
		ok, reason := admission(c.running, c.maxRunning, c.used, c.want, c.host, c.reserve)
		if ok != c.ok {
			t.Errorf("%s: ok=%v (%s)", c.name, ok, reason)
		}
		if !ok && reason == "" {
			t.Errorf("%s: a refusal needs a reason", c.name)
		}
	}
}

// --result writes one JSON record with the outcome vocabulary.
func TestWriteResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.json")
	writeResult(path, runResult{Box: "b", Outcome: OutcomeTimeout, ExitCode: ExitTimeout, Reason: "session ended by --timeout 1m",
		Started: time.Now(), Ended: time.Now(), Duration: "1m0s", Forwarded: []string{"ANTHROPIC_API_KEY"}})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["outcome"] != "timeout" || got["exit_code"] != float64(124) || got["box"] != "b" {
		t.Errorf("record: %v", got)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("result file mode %04o, want 0600", fi.Mode().Perm())
	}
	writeResult("", runResult{}) // no path: no-op
}

// The codes are the documented ones and do not collide with each other.
func TestExitCodesDocumented(t *testing.T) {
	seen := map[int]bool{}
	for _, c := range []int{ExitPreflightRefused, ExitAdmissionRefused, ExitUnreachable, ExitTimeout} {
		if c == 0 || c == 1 || seen[c] {
			t.Errorf("exit code %d reused or reserved", c)
		}
		seen[c] = true
	}
	for _, want := range []string{"78", "75", "69", "124", "run --result <file>"} {
		found := false
		for _, d := range exitCodeDocs {
			if d.Name == want {
				found = true
			}
		}
		if !found {
			t.Errorf("exit code %s not in exitCodeDocs", want)
		}
	}
}
