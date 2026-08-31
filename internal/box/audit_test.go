package box

import (
	"os"
	"testing"
)

func TestInvokedSubcommandNeverRecordsUserArgs(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })
	cases := map[string][]string{
		"run":             {"corral", "-C", "/p", "run", "--", "bash", "-c", "secret-looking text"},
		"snapshot create": {"corral", "snapshot", "create", "tag1"},
		"delete":          {"corral", "--box", "x", "delete", "--yes"},
		"dashboard":       {"corral"},
		"claude":          {"corral", "claude", "-p", "do things"},
	}
	for want, args := range cases {
		os.Args = args
		if got := invokedSubcommand(); got != want {
			t.Errorf("%v → %q, want %q", args, got, want)
		}
	}
	if p := parentProcess(); p == "" {
		t.Error("parentProcess must at least return the ppid")
	}
}
