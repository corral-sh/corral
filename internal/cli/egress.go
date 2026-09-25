package cli

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/corral-sh/corral/internal/box"
	"github.com/corral-sh/corral/internal/broker"
	"github.com/corral-sh/corral/internal/config"
	"github.com/corral-sh/corral/internal/ui"
)

// newBrokerCmd is the hidden child process: one allow-list proxy per box on
// the Mac's loopback. Started by StartBroker, stopped with SIGTERM. It reads
// the box's *current* configuration, so editing egress and running
// `corral restart` is how the list changes — the template does not.
func newBrokerCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:    "broker --box <name>",
		Short:  "Run the egress allow-list proxy for a box (internal; started automatically)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			b, err := openBoxByName(name)
			if err != nil {
				return err
			}
			allow, err := broker.Parse(box.EgressHosts(b.Cfg))
			if err != nil {
				return err
			}
			// Every decision goes to the per-box egress log; denials and
			// API calls also stay in the audit log, which the dashboard reads.
			box.RotateEgressLog(b.Name)
			srv := &broker.Server{
				Allow: allow,
				OnDeny: func(host string, port int) {
					hp := fmt.Sprintf("%s:%d", host, port)
					box.Audit(box.AuditEvent{Event: "egress-denied", Box: b.Name, Host: hp})
					box.LogEgressConnect(b.Name, hp, false)
				},
				OnAllow: func(host string, port int) {
					box.LogEgressConnect(b.Name, fmt.Sprintf("%s:%d", host, port), true)
				},
				APIs: map[string]*broker.APIRoute{},
				OnAPI: func(api, method, path string, status int, allowed bool) {
					ev := "api-call"
					if !allowed {
						ev = "api-denied"
					}
					box.Audit(box.AuditEvent{Event: ev, Box: b.Name, Host: api, Argv: []string{method, path}, Status: status})
					box.LogEgressAPI(b.Name, api, method, path, status, allowed)
				},
			}
			if b.Cfg.Network != config.NetworkBroker {
				srv.Allow = nil // no egress proxying: api routes only
			}
			// api_brokers: resolve each token here, on the Mac, once. A
			// missing credential is a hard failure — silently answering 401s
			// is what the caller would least expect.
			for _, ab := range b.Cfg.APIBrokers {
				token, err := b.ResolveHostVar(ab.Token)
				if err != nil {
					return fmt.Errorf("api_brokers[%s]: %w", ab.Name, err)
				}
				rules, err := broker.ParseAPIRules(ab.Allow)
				if err != nil {
					return err
				}
				up, err := url.Parse(ab.Upstream)
				if err != nil {
					return err
				}
				hdr, val := broker.APICredential(ab.Auth, ab.Header, ab.User, token)
				srv.APIs[ab.Name] = &broker.APIRoute{Name: ab.Name, Upstream: up, Allow: rules, Header: hdr, Value: val}
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
			defer stop()
			apis := make([]string, 0, len(b.Cfg.APIBrokers))
			for _, ab := range b.Cfg.APIBrokers {
				apis = append(apis, ab.Name+"→"+ab.Upstream)
			}
			fmt.Fprintf(os.Stdout, "corral broker for %s on %s; network %s; allow: %s; api: %s\n", b.Name, box.BrokerAddr(b.Name), b.Cfg.Network, strings.Join(box.EgressHosts(b.Cfg), " "), strings.Join(apis, " "))
			return srv.ListenAndServe(ctx, box.BrokerAddr(b.Name))
		},
	}
	cmd.Flags().StringVar(&name, "box", "", "box name")
	_ = cmd.MarkFlagRequired("box")
	return cmd
}

func newEgressCmd() *cobra.Command {
	var n int
	var showLog, jsonOut bool
	var since time.Duration
	cmd := &cobra.Command{
		Use:     "egress [box]",
		Short:   "Show a box's network mode, allowed destinations, what it reached and what was refused",
		GroupID: "insight",
		Long: `For network = "broker": the allow-list the proxy on your Mac enforces for this
box, whether the broker is running, every destination the box attempted through
it — allowed or denied, deduplicated with counts — and the api_brokers calls it
made (names, methods, paths and statuses only; never payloads).

The record behind it is ~/.corral/logs/egress-<box>.jsonl: one line per
attempted connection, per API call, and a marker at each session start and end
so a run can be sliced out. --log prints it in order; --since 24h limits both
views; --json is for scripts. The file is kept when the box is deleted
(corral gc removes it after 14 days) and rotated at 16 MiB.

A blocked install shows up as a denied destination; add the host to egress in
~/.corral/config.toml or ~/.corral/projects/<box>.toml and run
corral restart <box>.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := resolveBoxArg(args)
			if err != nil {
				return err
			}
			var from time.Time
			if since > 0 {
				from = time.Now().Add(-since)
			}
			if showLog {
				return printEgressLog(b, from, jsonOut)
			}
			if jsonOut {
				records, err := box.ReadEgressLog(b.Name, from)
				if err != nil {
					return err
				}
				return printJSON(egressJSON(b, records))
			}
			return printEgress(cmd.Context(), b, n, from)
		},
	}
	cmd.Flags().IntVar(&n, "denials", 20, "how many recent denials and API calls to show")
	cmd.Flags().BoolVar(&showLog, "log", false, "print the egress log line by line (oldest first) instead of the summary")
	cmd.Flags().DurationVar(&since, "since", 0, "only records newer than this (e.g. 2h, 30m); 0 = everything on file")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output (destinations summary, or records with --log)")
	return cmd
}

// egressReport is `corral egress --json`: the box's mode and allow-list
// plus every destination it attempted, deduplicated.
type egressReport struct {
	Box          string                  `json:"box"`
	Network      string                  `json:"network"`
	Allow        []string                `json:"allow"`
	Log          string                  `json:"log"`
	Since        *time.Time              `json:"since,omitempty"`
	Sessions     int                     `json:"sessions"`
	Destinations []box.EgressDestination `json:"destinations"`
}

func egressJSON(b *box.Box, records []box.EgressRecord) egressReport {
	r := egressReport{Box: b.Name, Network: b.Cfg.Network, Allow: box.EgressHosts(b.Cfg), Destinations: box.SummarizeEgress(records)}
	if r.Allow == nil {
		r.Allow = []string{}
	}
	if r.Destinations == nil {
		r.Destinations = []box.EgressDestination{}
	}
	r.Log, _ = box.EgressLogPath(b.Name)
	for _, rec := range records {
		if rec.Kind == "session" && rec.Event == "start" {
			r.Sessions++
		}
	}
	if len(records) > 0 {
		t := records[0].Time
		r.Since = &t
	}
	return r
}

// printEgressLog renders the raw log, one line per record, in the shape the
// tester asked for: <time> <box> <destination> allowed|denied.
func printEgressLog(b *box.Box, from time.Time, jsonOut bool) error {
	records, err := box.ReadEgressLog(b.Name, from)
	if err != nil {
		return err
	}
	if jsonOut {
		if records == nil {
			records = []box.EgressRecord{}
		}
		return printJSON(records)
	}
	if len(records) == 0 {
		p, _ := box.EgressLogPath(b.Name)
		fmt.Println(ui.Subtle.Render("no egress recorded for " + b.Name + " (" + ui.ShortenHome(p) + ")"))
		return nil
	}
	for _, r := range records {
		fmt.Println(egressLine(r))
	}
	return nil
}

func egressLine(r box.EgressRecord) string {
	ts := r.Time.Format("2006-01-02 15:04:05")
	verdict := func() string {
		if r.Allowed != nil && *r.Allowed {
			return "allowed"
		}
		return "denied"
	}
	switch r.Kind {
	case "connect":
		return fmt.Sprintf("%s %s %s %s", ts, r.Box, r.Host, verdict())
	case "api":
		return fmt.Sprintf("%s %s api:%s %s %s %d %s", ts, r.Box, r.Host, r.Method, r.Path, r.Status, verdict())
	case "session":
		detail := r.Event
		if r.Agent != "" {
			detail += " " + r.Agent
		}
		if r.Session != "" {
			detail += " " + r.Session
		}
		if r.ExitCode != nil {
			detail += fmt.Sprintf(" exit=%d", *r.ExitCode)
		}
		return fmt.Sprintf("%s %s session %s", ts, r.Box, detail)
	}
	return fmt.Sprintf("%s %s %s %s", ts, r.Box, r.Kind, r.Host)
}

func printEgress(_ context.Context, b *box.Box, n int, from time.Time) error {
	fmt.Println(ui.Header.Render("Egress · " + b.Name))
	ui.KV(os.Stdout, "network", networkLine(b))
	if !box.NeedsBroker(b.Cfg) {
		if b.Cfg.Network == config.NetworkFull {
			fmt.Println(ui.Subtle.Render("  Set network = \"broker\" (or profile = \"strict\") to route egress through an allow-list on your Mac;"))
			fmt.Println(ui.Subtle.Render("  api_brokers give the box scoped API access without the token entering it."))
		}
		return nil
	}
	state := ui.Bad.Render("not running") + ui.Subtle.Render("  — starts with the box: corral start "+b.Name)
	if box.BrokerReady(b.Name) {
		state = ui.Ok.Render("running")
		if b.Meta != nil && b.Meta.BrokerPID > 0 {
			state += ui.Subtle.Render(fmt.Sprintf("  pid %d", b.Meta.BrokerPID))
		}
	}
	ui.KV(os.Stdout, "broker", box.BrokerAddr(b.Name)+"  "+state)
	if p, err := box.BrokerLog(b.Name); err == nil {
		ui.KV(os.Stdout, "log", ui.ShortenHome(p))
	}
	events, err := box.ReadAudit(0)
	if err != nil {
		return err
	}
	if len(b.Cfg.APIBrokers) > 0 {
		fmt.Println(ui.Header.Render("API brokers") + ui.Subtle.Render("  (the token stays on the Mac; the box sees only these calls)"))
		for _, ab := range b.Cfg.APIBrokers {
			calls, denied := 0, 0
			for _, e := range events {
				if e.Box == b.Name && e.Host == ab.Name {
					switch e.Event {
					case "api-call":
						calls++
					case "api-denied":
						denied++
					}
				}
			}
			fmt.Printf("  %s  %s\n", ui.Bold.Render(ab.Name), ui.Subtle.Render(fmt.Sprintf("%s ← %s · %s · %d call(s), %d denied", ab.Upstream, ab.Token, box.APIBaseURL(b.Name, ab.Name), calls, denied)))
			for _, a := range ab.Allow {
				fmt.Println("    " + a)
			}
		}
		var recent []box.AuditEvent
		for _, e := range events {
			if e.Box == b.Name && (e.Event == "api-call" || e.Event == "api-denied") {
				recent = append(recent, e)
			}
		}
		if len(recent) > n {
			recent = recent[len(recent)-n:]
		}
		for _, e := range recent {
			mark := ui.Ok.Render("✓")
			if e.Event == "api-denied" {
				mark = ui.Bad.Render("✗")
			}
			fmt.Printf("  %s %s  %s %s %s\n", mark, ui.Subtle.Render(e.Time.Format("2006-01-02 15:04:05")), e.Host, strings.Join(e.Argv, " "), ui.Subtle.Render(fmt.Sprint(e.Status)))
		}
	}
	if b.Cfg.Network != config.NetworkBroker {
		return nil
	}
	fmt.Println(ui.Header.Render("Allowed destinations") + ui.Subtle.Render("  (ports 80/443 unless :port given; *.suffix matches subdomains only)"))
	for _, h := range box.EgressHosts(b.Cfg) {
		fmt.Println("  " + h)
	}
	if err := printEgressDestinations(b, from); err != nil {
		return err
	}
	var denials []box.AuditEvent
	for _, e := range events {
		if e.Event == "egress-denied" && e.Box == b.Name {
			denials = append(denials, e)
		}
	}
	if len(denials) > n {
		denials = denials[len(denials)-n:]
	}
	fmt.Println(ui.Header.Render(fmt.Sprintf("Recent denials (%d)", len(denials))))
	if len(denials) == 0 {
		fmt.Println(ui.Subtle.Render("  none"))
		return nil
	}
	for _, d := range denials {
		fmt.Printf("  %s  %s\n", ui.Subtle.Render(d.Time.Format("2006-01-02 15:04:05")), d.Host)
	}
	fmt.Println(ui.Subtle.Render("  To allow one: add it to egress in ~/.corral/projects/" + b.Name + ".toml, then corral restart " + b.Name))
	return nil
}

// printEgressDestinations is the "Destinations attempted" section: every destination the box
// attempted through its broker, deduplicated, denied first — the measurement
// behind "which hosts does this repository actually need?".
func printEgressDestinations(b *box.Box, from time.Time) error {
	records, err := box.ReadEgressLog(b.Name, from)
	if err != nil {
		return err
	}
	logPath, _ := box.EgressLogPath(b.Name)
	scope := "all on file"
	if !from.IsZero() {
		scope = "since " + from.Format("2006-01-02 15:04")
	}
	dests := box.SummarizeEgress(records)
	fmt.Println(ui.Header.Render(fmt.Sprintf("Destinations attempted (%d)", len(dests))) + ui.Subtle.Render("  "+scope+" · "+ui.ShortenHome(logPath)))
	if len(dests) == 0 {
		fmt.Println(ui.Subtle.Render("  none recorded yet — connections the box makes through the broker appear here, allowed or not"))
		return nil
	}
	for _, d := range dests {
		mark, word := ui.Ok.Render("✓"), "allowed"
		if !d.Allowed {
			mark, word = ui.Bad.Render("✗"), "denied"
		}
		host := d.Host
		if d.Kind == "api" {
			host = "api:" + d.Host
		}
		fmt.Printf("  %s %-44s %s  %s\n", mark, host, ui.Subtle.Render(fmt.Sprintf("%-7s ×%-5d", word, d.Count)), ui.Subtle.Render("last "+d.Last.Format("2006-01-02 15:04:05")))
	}
	fmt.Println(ui.Subtle.Render("  corral egress " + b.Name + " --log prints every attempt in order; --since 24h narrows; --json for scripts"))
	return nil
}
