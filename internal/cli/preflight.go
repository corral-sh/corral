package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/corral-sh/corral/internal/box"
	"github.com/corral-sh/corral/internal/config"
	guestpkg "github.com/corral-sh/corral/internal/guest"
	"github.com/corral-sh/corral/internal/ui"
)

// boxPreflight answers the question `doctor` on the host cannot: do the things
// this repository declared actually work from inside the box right now — the
// agent, each toolchain, the git hosts and egress entries, the in-guest
// controls, the granted variables? A run that starts on a broken environment
// does not stop, it improvises. Every check is a single guest command
// through a login shell so PATH and proxy variables are the session's.
func boxPreflight(ctx context.Context, b *box.Box, hostEnv map[string]string) []check {
	var out []check
	guest := func(script string) (string, bool) {
		o, err := b.Lima.Run(ctx, b.Name, "bash", "-lc", script)
		return strings.TrimSpace(o), err == nil
	}
	firstLine := func(s string) string {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			return s[:i]
		}
		return s
	}
	lastLine := func(s string) string {
		s = strings.TrimSpace(s)
		if i := strings.LastIndexByte(s, '\n'); i >= 0 {
			return strings.TrimSpace(s[i+1:])
		}
		return s
	}

	// Agents.
	for _, a := range b.Agents() {
		if v := a.VersionArgv(); len(v) > 0 {
			o, ok := guest(strings.Join(v, " ") + " 2>&1")
			out = append(out, check{"agent " + a.Name(), ok, firstLine(o), "corral rebuild " + b.Name})
		}
	}
	// Toolchains.
	probe := map[string]string{
		"node": "node --version", "go": "go version", "python": "python3 --version", "docker": "docker version --format '{{.Server.Version}}' 2>&1 || docker --version",
		"java": "java -version 2>&1", "android": "adb --version 2>&1", "flutter": "flutter --version 2>&1",
	}
	for _, tc := range b.Cfg.Toolchains {
		o, ok := guest(probe[tc] + " 2>&1")
		detail := firstLine(o)
		if v, pinned := b.Cfg.ToolchainVersions[tc]; pinned {
			version, _ := config.SplitToolchainVersion(v)
			if !strings.Contains(o, version) {
				ok = false
			}
			detail += "  (pinned " + version + ")"
		}
		out = append(out, check{"toolchain " + tc, ok, detail, "corral rebuild " + b.Name})
	}
	// In-guest controls that this configuration expects. A unit without its
	// .conf was never configured for this box and is not a check at all —
	// only a configured unit that is not active is a failure.
	for _, unit := range []string{"git-shadow", "boxdirs", "hide", "offline", "broker"} {
		o, _ := guest("if [ -f /etc/corral/" + unit + ".conf ]; then systemctl is-active corral-" + unit + ".service 2>&1; else echo n/a; fi")
		state := lastLine(o)
		if state == "n/a" || state == "" {
			continue
		}
		out = append(out, check{"control " + unit, state == "active", state, "corral shell, then journalctl -u corral-" + unit})
	}
	// api_brokers: the route must answer from inside the box. A 403
	// from the broker itself ("api-denied") still proves the path works.
	for _, ab := range b.Cfg.APIBrokers {
		code, _ := guest("curl -s -o /dev/null -w '%{http_code}' --max-time 8 " + guestpkg.ShellQuote(box.APIBaseURL(b.Name, ab.Name)+"/") + " 2>/dev/null")
		ok := code != "" && code != "000"
		detail := "HTTP " + code + " via " + box.APIBaseURL(b.Name, ab.Name)
		if !ok {
			detail = "broker not reachable from the box"
		}
		out = append(out, check{"api " + ab.Name, ok, detail, "corral restart " + b.Name + " (starts the broker on the Mac; `corral egress " + b.Name + "` shows its log)"})
	}
	// Network: git hosts and egress entries, through whatever the box has.
	switch b.Cfg.Network {
	case config.NetworkOffline:
		_, reached := guest("curl -s -o /dev/null --max-time 8 https://1.1.1.1/")
		out = append(out, check{"offline: egress rejected", !reached, "network = offline", "corral rebuild (the nftables unit did not apply)"})
	default:
		seen := map[string]bool{}
		try := func(label, host string) {
			if seen[host] {
				return
			}
			seen[host] = true
			if strings.HasPrefix(host, "*.") {
				out = append(out, check{label + " " + host, true, "wildcard — not probed", ""})
				return
			}
			h := host
			if !strings.Contains(h, ":") {
				h += ":443"
			}
			code, _ := guest("curl -s -o /dev/null -w '%{http_code}' --max-time 15 https://" + h + "/")
			ok := code != "" && code != "000"
			detail := "HTTP " + code
			if !ok {
				detail = "unreachable from the box"
			}
			fix := "check `corral egress " + b.Name + "` and your network"
			if b.Cfg.Network == config.NetworkBroker {
				fix = "add it to `egress` in ~/.corral/projects/" + b.Name + ".toml, then corral restart " + b.Name
			}
			out = append(out, check{label + " " + host, ok, detail, fix})
		}
		for host := range b.Cfg.GitTokens {
			try("git host", host)
		}
		if b.Cfg.Network == config.NetworkBroker {
			for _, h := range box.EgressHosts(b.Cfg) {
				try("egress", h)
			}
		}
	}
	// Granted variables: present on the host (names only, never values).
	var names []string
	seenName := map[string]bool{}
	addName := func(k string) {
		if !seenName[k] {
			seenName[k] = true
			names = append(names, k)
		}
	}
	for _, k := range b.Cfg.ForwardEnv {
		addName(k)
	}
	for _, a := range b.Agents() {
		for _, k := range a.ForwardEnv() {
			addName(k)
		}
	}
	if b.Cfg.EnvFile != "" {
		file, err := box.LoadEnvFile(b.Cfg.EnvFile)
		detail := fmt.Sprintf("%d variable(s), 0600, yours", len(file))
		if err != nil {
			detail = err.Error()
		} else {
			for k, v := range file {
				if hv, ok := hostEnv[k]; !ok || hv == "" {
					hostEnv[k] = v // the launcher consults the file after the host env
				}
			}
		}
		out = append(out, check{"env_file " + ui.ShortenHome(b.Cfg.EnvFile), err == nil, detail, "chmod 0600 " + b.Cfg.EnvFile + " and keep it under your home directory"})
	}
	for _, e := range b.Cfg.EnvFromHost {
		_, hostVar, _ := strings.Cut(e, "=")
		v, ok := hostEnv[hostVar]
		out = append(out, check{"env_from_host " + e, ok && v != "", "", "export " + hostVar + " on the Mac — the session refuses to start without it"})
	}
	for _, k := range b.Cfg.KeychainEnv {
		if v, ok := hostEnv[k]; ok && v != "" {
			out = append(out, check{"keychain_env " + k, true, "set in the environment", ""})
			continue
		}
		v, err := box.KeychainLookup(k)
		detail, fix := "Keychain item", ""
		if err != nil {
			detail, fix = err.Error(), box.KeychainRemedy(k, err)
		} else if v == "" {
			detail, fix = "the Keychain item is empty", box.KeychainRemedy(k, box.ErrKeychainNotFound)
		}
		out = append(out, check{"keychain_env " + k, err == nil && v != "", detail, fix})
	}
	present := 0
	for _, k := range names {
		if v, ok := hostEnv[k]; ok && v != "" {
			present++
		}
	}
	out = append(out, check{"agent credentials", true, fmt.Sprintf("%d of %d forwardable variable(s) set on the Mac (%s)", present, len(names), joinNames(names)), ""})
	// Provision scripts of this boot.
	if len(b.Cfg.Provision) > 0 {
		fails, err := b.ProvisionFailures(ctx)
		out = append(out, check{"provision scripts", err == nil && len(fails) == 0, strings.Join(fails, "; "), "corral logs " + b.Name})
	}
	return out
}

func printChecks(w io.Writer, checks []check, asJSON bool) (bad int) {
	for _, c := range checks {
		if !c.ok {
			bad++
		}
	}
	if asJSON {
		type jc struct {
			Name   string `json:"name"`
			OK     bool   `json:"ok"`
			Detail string `json:"detail,omitempty"`
			Fix    string `json:"fix,omitempty"`
		}
		rows := make([]jc, len(checks))
		for i, c := range checks {
			rows[i] = jc{c.name, c.ok, c.detail, c.fix}
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rows)
		return bad
	}
	for _, c := range checks {
		mark := ui.Ok.Render("✓")
		if !c.ok {
			mark = ui.Bad.Render("✗")
		}
		fmt.Fprintf(w, "  %s %-34s %s\n", mark, c.name, ui.Subtle.Render(c.detail))
		if !c.ok && c.fix != "" {
			fmt.Fprintf(w, "      %s %s\n", ui.Warn.Render("fix:"), c.fix)
		}
	}
	return bad
}

// runBoxDoctor boots the box if needed and prints the preflight.
func runBoxDoctor(ctx context.Context, b *box.Box, asJSON bool) error {
	if err := ensureRunning(ctx, b); err != nil {
		return err
	}
	if !asJSON {
		fmt.Fprintf(os.Stdout, "%s %s\n", ui.Logo, ui.Title.Render("Preflight: "+b.Name+"  ·  "+ui.ShortenHome(b.Project)))
	}
	checks := boxPreflight(ctx, b, box.HostEnvMap(os.Environ()))
	if bad := printChecks(os.Stdout, checks, asJSON); bad > 0 {
		return fmt.Errorf("%d preflight check(s) failed in %s", bad, b.Name)
	}
	if !asJSON {
		ui.Success(os.Stdout, "everything this project declared works from inside the box")
	}
	return nil
}
