package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// tomlBlockRe matches a fenced ```toml block in markdown.
var tomlBlockRe = regexp.MustCompile("(?s)```toml\n(.*?)```")

// Every TOML example in the docs must parse: readers copy them into their
// config, and an invalid one fails there, not here.
func TestDocsTOMLExamplesParse(t *testing.T) {
	root := filepath.Join("..", "..")
	files, _ := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	files = append(files, filepath.Join(root, "README.md"), filepath.Join(root, "CONTRIBUTING.md"))
	checked := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Skipf("%s not found (not running from the repo)", f)
		}
		s := string(b)
		for _, m := range tomlBlockRe.FindAllStringSubmatchIndex(s, -1) {
			checked++
			var v map[string]any
			if _, err := toml.Decode(s[m[2]:m[3]], &v); err != nil {
				t.Errorf("%s:%d: toml example does not parse: %v", filepath.Base(f), strings.Count(s[:m[0]], "\n")+1, err)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no ```toml blocks found — has the docs layout changed?")
	}
}
