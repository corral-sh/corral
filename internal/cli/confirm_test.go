package cli

import (
	"strings"
	"testing"
)

// Without a terminal, a destructive command must fail and name --yes instead
// of defaulting to "no" and exiting 0 having done nothing.
func TestConfirmDestructiveWithoutTerminal(t *testing.T) {
	old := canPrompt
	t.Cleanup(func() { canPrompt = old })
	canPrompt = func() bool { return false }

	ok, err := confirmDestructive(false, "Delete box x?", "corral delete x")
	if ok || err == nil {
		t.Fatalf("no terminal: want refusal with an error, got ok=%v err=%v", ok, err)
	}
	if !strings.Contains(err.Error(), "`corral delete x --yes`") {
		t.Errorf("error must name the command to re-run: %v", err)
	}

	ok, err = confirmDestructive(true, "Delete box x?", "corral delete x")
	if !ok || err != nil {
		t.Errorf("--yes must proceed without asking: ok=%v err=%v", ok, err)
	}
}
