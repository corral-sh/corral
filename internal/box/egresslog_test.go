package box

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEgressLogRoundTripAndSummary(t *testing.T) {
	t.Setenv("CORRAL_HOME", filepath.Join(t.TempDir(), "eb"))
	const b = "alpha-1234"
	LogEgressConnect(b, "api.anthropic.com:443", true)
	LogEgressConnect(b, "api.anthropic.com:443", true)
	LogEgressConnect(b, "evil.example:443", false)
	LogEgressAPI(b, "gitlab", "GET", "/api/v4/projects", 200, true)
	LogEgressAPI(b, "gitlab", "POST", "/api/v4/projects", 403, false)
	code := 0
	LogEgress(EgressRecord{Box: b, Kind: "session", Event: "end", Agent: "claude", Session: "s1", ExitCode: &code})
	LogEgress(EgressRecord{Box: "", Kind: "connect", Host: "ignored:443"}) // no box: dropped
	LogEgressConnect("other-box", "x.test:443", true)                      // another box's file

	recs, err := ReadEgressLog(b, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 6 {
		t.Fatalf("want 6 records, got %d: %+v", len(recs), recs)
	}
	for _, r := range recs {
		if r.Box != b || r.Time.IsZero() || r.PID == 0 {
			t.Errorf("record missing box/time/pid: %+v", r)
		}
	}
	if recs[2].Host != "evil.example:443" || recs[2].Allowed == nil || *recs[2].Allowed {
		t.Errorf("denied connect not recorded as denied: %+v", recs[2])
	}
	if recs[4].Kind != "api" || recs[4].Method != "POST" || recs[4].Status != 403 {
		t.Errorf("api record: %+v", recs[4])
	}
	if recs[5].Kind != "session" || recs[5].ExitCode == nil {
		t.Errorf("session marker: %+v", recs[5])
	}

	sum := SummarizeEgress(recs)
	// Denied first, then allowed by count; session markers are not destinations.
	want := []struct {
		host    string
		allowed bool
		count   int
	}{
		{"evil.example:443", false, 1},
		{"gitlab", false, 1},
		{"api.anthropic.com:443", true, 2},
		{"gitlab", true, 1},
	}
	if len(sum) != len(want) {
		t.Fatalf("summary %+v", sum)
	}
	for i, w := range want {
		if sum[i].Host != w.host || sum[i].Allowed != w.allowed || sum[i].Count != w.count {
			t.Errorf("summary[%d] = %+v, want %+v", i, sum[i], w)
		}
	}
	if sum[2].First.After(sum[2].Last) {
		t.Errorf("first/last inverted: %+v", sum[2])
	}

	// since filters by time.
	future, err := ReadEgressLog(b, time.Now().Add(time.Hour))
	if err != nil || len(future) != 0 {
		t.Errorf("since in the future should yield nothing: %v %d", err, len(future))
	}
	// A box with no log is not an error.
	none, err := ReadEgressLog("never-existed", time.Time{})
	if err != nil || none != nil {
		t.Errorf("missing log: %v %v", err, none)
	}
}

func TestEgressLogRotateAndPrune(t *testing.T) {
	t.Setenv("CORRAL_HOME", filepath.Join(t.TempDir(), "eb"))
	const b = "beta-5678"
	LogEgressConnect(b, "a.test:443", true)
	p, _ := EgressLogPath(b)

	// Below the threshold: nothing moves.
	RotateEgressLog(b)
	if _, err := os.Stat(p + ".1"); err == nil {
		t.Fatal("rotated a small log")
	}
	// Grow past the threshold: one generation kept, and reads span both.
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.Write(make([]byte, EgressLogRotateBytes))
	f.Close()
	RotateEgressLog(b)
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Fatal("log not rotated")
	}
	LogEgressConnect(b, "b.test:443", true)
	recs, err := ReadEgressLog(b, time.Time{})
	if err != nil || len(recs) != 2 || recs[0].Host != "a.test:443" || recs[1].Host != "b.test:443" {
		t.Fatalf("read across rotation: %v %+v", err, recs)
	}

	// Prune: an existing box keeps its log; a deleted box's log goes only once old.
	LogEgressConnect("gone-0001", "c.test:443", true)
	gp, _ := EgressLogPath("gone-0001")
	if got := PruneEgressLogs(map[string]bool{b: true}, time.Hour); len(got) != 0 {
		t.Fatalf("pruned a fresh log: %v", got)
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, f := range []string{gp, p, p + ".1"} {
		_ = os.Chtimes(f, old, old)
	}
	got := PruneEgressLogs(map[string]bool{b: true}, time.Hour)
	if len(got) != 1 || got[0] != "gone-0001" {
		t.Fatalf("prune = %v, want [gone-0001]", got)
	}
	if _, err := os.Stat(gp); !os.IsNotExist(err) {
		t.Error("deleted box's log still present")
	}
	if _, err := os.Stat(p); err != nil {
		t.Error("existing box's log was pruned")
	}
	// Now the box is gone too: both generations go.
	got = PruneEgressLogs(map[string]bool{}, time.Hour)
	if len(got) != 1 || got[0] != b {
		t.Fatalf("prune = %v, want [%s]", got, b)
	}
	if _, err := os.Stat(p + ".1"); !os.IsNotExist(err) {
		t.Error("rotated generation survived the prune")
	}
}
