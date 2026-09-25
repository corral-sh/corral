package cli

import (
	"fmt"
	"os"

	"github.com/corral-sh/corral/internal/ui"
)

// canPrompt is ui.CanPrompt; a variable so tests can take the terminal away.
var canPrompt = ui.CanPrompt

// confirmDestructive gates a destructive action. It proceeds when --yes was
// given or the user answers yes. A "no" typed at the prompt is a quiet false:
// the user saw the question. With no terminal to ask on (a script, CI, an
// agent's shell) it fails instead of defaulting to no — doing nothing and
// exiting 0 reads as success to whoever ran it. rerun is the command to
// repeat with --yes.
func confirmDestructive(yes bool, question, rerun string) (bool, error) {
	if yes {
		return true, nil
	}
	if !canPrompt() {
		return false, fmt.Errorf("nothing done: this needs confirmation and there is no terminal to ask on — re-run with `%s --yes`", rerun)
	}
	return ui.Confirm(os.Stderr, question, false), nil
}
