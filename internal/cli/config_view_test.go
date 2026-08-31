package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/corral-sh/corral/internal/config"
	"github.com/corral-sh/corral/internal/policy"
)

// `corral config` must show every key the configuration schema has (regression test:
// keychain_env and box_dirs were missing, so a refused box_dirs entry was
// invisible). Deprecated aliases are the only exception.
func TestResolvedViewShowsEveryKey(t *testing.T) {
	cfg, err := config.Resolve(config.Merge(config.Defaults(), config.File{
		KeychainEnv: []string{"GITLAB_NPM_TOKEN"},
		BoxDirs:     []string{"node_modules", "../escape"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	printResolved(&buf, cfg)
	out := buf.String()
	skip := map[string]bool{"shared_agent_state": true} // deprecated alias of agent_state
	for _, k := range policy.FileKeys() {
		if skip[k] {
			continue
		}
		if !strings.Contains(out, k) {
			t.Errorf("resolved view does not show %q", k)
		}
	}
	for _, want := range []string{"GITLAB_NPM_TOKEN", "node_modules", "../escape", "refused"} {
		if !strings.Contains(out, want) {
			t.Errorf("resolved view missing %q:\n%s", want, out)
		}
	}
}
