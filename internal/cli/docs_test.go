package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	_ "github.com/corral-sh/corral/internal/agent/claude" // the catalog lists agent commands; main.go does this import
	"github.com/corral-sh/corral/internal/config"
)

// Adding a config key means describing it: the catalog is how a new user (or
// their AI assistant) discovers the feature.
func TestEveryConfigKeyDocumented(t *testing.T) {
	ft := reflect.TypeOf(config.File{})
	for i := 0; i < ft.NumField(); i++ {
		key, _, _ := strings.Cut(ft.Field(i).Tag.Get("toml"), ",")
		if key == "" {
			continue
		}
		if keyDocs[key] == "" {
			t.Errorf("config key %q has no entry in keyDocs (internal/cli/docs.go)", key)
		}
	}
	for k := range keyDocs {
		found := false
		for i := 0; i < ft.NumField(); i++ {
			if key, _, _ := strings.Cut(ft.Field(i).Tag.Get("toml"), ","); key == k {
				found = true
			}
		}
		if !found {
			t.Errorf("keyDocs documents %q, which is not a config key", k)
		}
	}
	for _, tc := range config.KnownToolchains {
		if toolchainDocs[tc] == "" {
			t.Errorf("toolchain %q has no entry in toolchainDocs", tc)
		}
	}
}

// docs/FEATURES.md is the checked-in rendering of `corral docs`; `make docs`
// regenerates it. Stale = the catalog changed without the docs.
func TestFeaturesCatalogUpToDate(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("..", "..", "docs", "FEATURES.md"))
	if err != nil {
		t.Skip("docs/FEATURES.md not found (not running from the repo)")
	}
	var buf bytes.Buffer
	renderCatalogMarkdown(&buf, buildCatalog(newRoot()))
	if buf.String() != string(want) {
		t.Fatalf("docs/FEATURES.md is stale — run `make docs` and commit the result")
	}
	c := buildCatalog(newRoot())
	if len(c.Commands) < 20 || len(c.Config) < 30 {
		t.Errorf("catalog looks incomplete: %d commands, %d keys", len(c.Commands), len(c.Config))
	}
}
