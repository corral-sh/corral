package cli

import (
	"slices"
	"testing"
)

// A Homebrew upgrade refreshes the taps first: Homebrew's own auto-update runs
// at most once a day, so a release pushed since then would read as
// "already installed".
func TestBrewUpgradeUpdatesFirst(t *testing.T) {
	if len(brewUpgradeSteps) != 2 {
		t.Fatalf("steps: %v", brewUpgradeSteps)
	}
	if !slices.Equal(brewUpgradeSteps[0], []string{"brew", "update"}) {
		t.Errorf("first step must be brew update, got %v", brewUpgradeSteps[0])
	}
	if !slices.Contains(brewUpgradeSteps[1], "corral-sh/tap/corral") {
		t.Errorf("upgrade must name the fully qualified formula (bare corral is a different formula): %v", brewUpgradeSteps[1])
	}
}
