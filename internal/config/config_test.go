package config

import (
	"github.com/BurntSushi/toml"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultsResolve(t *testing.T) {
	c, err := Resolve(Defaults())
	if err != nil {
		t.Fatal(err)
	}
	if c.DefaultAgent != "claude" || c.CPUs != 4 || !c.Yolo || !c.SharedAgentState {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestValidation(t *testing.T) {
	cases := map[string]File{
		"cpus":      {CPUs: ptr(0)},
		"memory":    {Memory: ptr("lots")},
		"toolchain": {Toolchains: []string{"rust"}},
		"env":       {Env: []string{"1BAD=x"}},
		"alias":     {EnvFromHost: []string{"A=$B"}},
		"dup":       {Env: []string{"A=1"}, EnvFromHost: []string{"A=B"}},
		"name":      {Name: ptr("Bad_Name")},
		"gittoken":  {GitTokens: map[string]GitToken{"host": {Token: "not valid"}}},
		"gituser":   {GitTokens: map[string]GitToken{"host": {Token: "TOK", User: "bad user\n"}}},
		"reserve":   {MemoryReserve: ptr("lots")},
		"tcver-key": {ToolchainVersions: map[string]string{"python": "3.12"}},
		"tcver-maj": {ToolchainVersions: map[string]string{"node": "22"}},
		"tcver-v":   {ToolchainVersions: map[string]string{"node": "v22.23.2"}},
		"tcver-nsh": {ToolchainVersions: map[string]string{"node": "22.23.2@0123456789abcdef0123456789abcdef01234567"}},
		"tcver-go":  {ToolchainVersions: map[string]string{"go": "go1.25.14"}},
		"tcver-inj": {ToolchainVersions: map[string]string{"go": "1.25.14 && curl x"}},
		"tcver-val": {ToolchainVersions: map[string]string{"flutter": "latest; rm -rf /"}},
		"tcver-sha": {ToolchainVersions: map[string]string{"flutter": "3.44.2@abc"}},
		"api-name":  {APIBrokers: []APIBroker{{Name: "Git Lab", Upstream: "https://x", Token: "T", Header: "H", Allow: []string{"GET /"}}}},
		"api-http":  {APIBrokers: []APIBroker{{Name: "gitlab", Upstream: "http://x", Token: "T", Header: "H", Allow: []string{"GET /"}}}},
		"api-path":  {APIBrokers: []APIBroker{{Name: "gitlab", Upstream: "https://x/api", Token: "T", Header: "H", Allow: []string{"GET /"}}}},
		"api-hdr":   {APIBrokers: []APIBroker{{Name: "gitlab", Upstream: "https://x", Token: "T", Allow: []string{"GET /"}}}},
		"api-allow": {APIBrokers: []APIBroker{{Name: "gitlab", Upstream: "https://x", Token: "T", Header: "H"}}},
		"api-rule":  {APIBrokers: []APIBroker{{Name: "gitlab", Upstream: "https://x", Token: "T", Header: "H", Allow: []string{"get /x"}}}},
		"api-basic": {APIBrokers: []APIBroker{{Name: "jira", Upstream: "https://x", Token: "T", Auth: "basic", Allow: []string{"GET /"}}}},
		"api-off":   {Network: ptr(NetworkOffline), APIBrokers: []APIBroker{{Name: "gitlab", Upstream: "https://x", Token: "T", Header: "H", Allow: []string{"GET /"}}}},
		"maxrun":    {MaxRunning: ptr(-1)},
		"timeout":   {Timeout: ptr("10s")},
	}
	for name, f := range cases {
		if _, err := Resolve(Merge(Defaults(), f)); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

// node and go are pinnable like flutter; the accepted shapes.
func TestToolchainVersionsAccepted(t *testing.T) {
	for _, v := range []map[string]string{
		{"node": "22.23.2", "go": "1.25.14"},
		{"go": "1.20"},
		{"flutter": "3.44.2@0123456789abcdef0123456789abcdef01234567"},
	} {
		if _, err := Resolve(Merge(Defaults(), File{ToolchainVersions: v})); err != nil {
			t.Errorf("%v: %v", v, err)
		}
	}
}

func TestParseMount(t *testing.T) {
	home, _ := os.UserHomeDir()
	m, err := ParseMount("~/x:/y:ro")
	if err != nil {
		t.Fatal(err)
	}
	if m.Host != filepath.Join(home, "x") || m.Guest != "/y" || m.Writable {
		t.Errorf("%+v", m)
	}
	if _, err := ParseMount("relative:/y"); err == nil {
		t.Error("relative host path should fail")
	}
	if _, err := ParseMount("/a:/b:zz"); err == nil {
		t.Error("bad mode should fail")
	}
	m, _ = ParseMount("/a")
	if m.Guest != "/a" || !m.Writable {
		t.Errorf("bare mount: %+v", m)
	}
}

// git_tokens accepts the bare-string form and the { token, user } table.
func TestGitTokenForms(t *testing.T) {
	var f File
	src := `git_tokens = { "a.example.com" = "A_TOKEN", "b.example.com" = { token = "B_TOKEN", user = "gitlab+deploy-token-1" } }` + "\n"
	if _, err := toml.Decode(src, &f); err != nil {
		t.Fatal(err)
	}
	if f.GitTokens["a.example.com"] != (GitToken{Token: "A_TOKEN"}) || f.GitTokens["b.example.com"] != (GitToken{Token: "B_TOKEN", User: "gitlab+deploy-token-1"}) {
		t.Errorf("decoded %+v", f.GitTokens)
	}
	for _, bad := range []string{`git_tokens = { "a" = { user = "x" } }`, `git_tokens = { "a" = { token = "T", extra = 1 } }`, `git_tokens = { "a" = 5 }`} {
		if _, err := toml.Decode(bad+"\n", &File{}); err == nil {
			t.Errorf("%s should be refused", bad)
		}
	}
}

// toolchains = [] opts out of the default node; an omitted key keeps it.
func TestEmptyToolchainsReplacesDefault(t *testing.T) {
	var explicit, omitted File
	if _, err := toml.Decode("toolchains = []\n", &explicit); err != nil {
		t.Fatal(err)
	}
	if _, err := toml.Decode("cpus = 2\n", &omitted); err != nil {
		t.Fatal(err)
	}
	if got := Merge(Defaults(), explicit).Toolchains; len(got) != 0 {
		t.Errorf("explicit [] must replace the default, got %v", got)
	}
	if got := Merge(Defaults(), omitted).Toolchains; len(got) != 1 || got[0] != "node" {
		t.Errorf("omitted key must keep the default, got %v", got)
	}
	// A later layer can add again on top of an emptied list.
	if got := Merge(Merge(Defaults(), explicit), File{Toolchains: []string{"go"}}).Toolchains; len(got) != 1 || got[0] != "go" {
		t.Errorf("adding after [] : %v", got)
	}
}

// agents works like toolchains, and default_agent is always installed.
func TestAgentsLayering(t *testing.T) {
	var empty File
	if _, err := toml.Decode("agents = []\ndefault_agent = \"codex\"\n", &empty); err != nil {
		t.Fatal(err)
	}
	c, err := Resolve(Merge(Merge(Defaults(), File{Agents: []string{"codex"}}), empty))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.Agents, ",") != "codex" || strings.Join(c.TrustedAgents, ",") != "codex" {
		t.Errorf("agents = [] + default_agent = codex: agents %v, trusted %v", c.Agents, c.TrustedAgents)
	}
	c, err = Resolve(Merge(Defaults(), File{Agents: []string{"codex"}}))
	if err != nil || strings.Join(c.Agents, ",") != "claude,codex" {
		t.Errorf("agents unioned with the default: %v %v", c.Agents, err)
	}
	if _, err := Resolve(Merge(Defaults(), File{Agents: []string{"Bad Name"}})); err == nil {
		t.Error("an agents entry that is not a name must be refused")
	}
}

func TestParseTimeout(t *testing.T) {
	if d, err := ParseTimeout(""); err != nil || d != 0 {
		t.Errorf("empty: %v %v", d, err)
	}
	if d, err := ParseTimeout("45m"); err != nil || d != 45*time.Minute {
		t.Errorf("45m: %v %v", d, err)
	}
	for _, bad := range []string{"10s", "soon", "-1h"} {
		if _, err := ParseTimeout(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

// api_brokers: the table form decodes, auth defaults to header, later layers
// override by name.
func TestAPIBrokersDecodeAndMerge(t *testing.T) {
	var f File
	src := `
[[api_brokers]]
name = "gitlab"
upstream = "https://git.example.com"
token = "GITLAB_TOKEN"
header = "PRIVATE-TOKEN"
allow = ["GET /api/v4/projects/42/**"]

[[api_brokers]]
name = "jira"
upstream = "https://x.atlassian.net"
token = "JIRA_TOKEN"
auth = "basic"
user = "me@example.com"
allow = ["GET /rest/api/3/issue/*"]
`
	if _, err := toml.Decode(src, &f); err != nil {
		t.Fatal(err)
	}
	c, err := Resolve(Merge(Defaults(), f))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.APIBrokers) != 2 || c.APIBrokers[0].Auth != APIAuthHeader || c.APIBrokers[1].Auth != APIAuthBasic {
		t.Errorf("%+v", c.APIBrokers)
	}
	over := File{APIBrokers: []APIBroker{{Name: "gitlab", Upstream: "https://git.example.com", Token: "OTHER", Header: "PRIVATE-TOKEN", Allow: []string{"GET /version"}}}}
	m := Merge(Merge(Defaults(), f), over)
	if len(m.APIBrokers) != 2 || m.APIBrokers[0].Token != "OTHER" || m.APIBrokers[1].Name != "jira" {
		t.Errorf("merge by name: %+v", m.APIBrokers)
	}
}
