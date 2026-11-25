package cli

import (
	"strings"
	"testing"

	"github.com/corral-sh/corral/internal/agent"
)

// Agent arguments reach the agent whether its command is typed by name or by
// alias: `corral agy -p x` used to drop `-p x`, because only cmd.Name() was
// searched for in os.Args.
func TestAgentPassthroughArgsViaAlias(t *testing.T) {
	a, ok := agent.Lookup("antigravity")
	if !ok {
		t.Fatal("antigravity not registered")
	}
	cmd := newAgentCmd(a)
	if strings.Join(cmd.Aliases, ",") != "agy" {
		t.Fatalf("aliases = %v", cmd.Aliases)
	}
	for _, tc := range []struct {
		called string
		argv   []string
	}{
		{"antigravity", []string{"corral", "antigravity", "--", "-p", "x"}},
		{"agy", []string{"corral", "agy", "--", "-p", "x"}},
		{"agy", []string{"corral", "--box", "b1", "agy", "--yes", "-p", "x"}},
	} {
		if got := strings.Join(passthroughAfter(cmd, tc.argv, tc.called), " "); got != "-p x" {
			t.Errorf("%q: agent args = %q, want \"-p x\"", tc.argv, got)
		}
	}
}
