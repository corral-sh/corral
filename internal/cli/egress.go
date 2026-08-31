package cli

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

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
			srv := &broker.Server{
				Allow: allow,
				OnDeny: func(host string, port int) {
					box.Audit(box.AuditEvent{Event: "egress-denied", Box: b.Name, Host: fmt.Sprintf("%s:%d", host, port)})
				},
				APIs: map[string]*broker.APIRoute{},
				OnAPI: func(api, method, path string, status int, allowed bool) {
					ev := "api-call"
					if !allowed {
						ev = "api-denied"
					}
					box.Audit(box.AuditEvent{Event: ev, Box: b.Name, Host: api, Argv: []string{method, path}, Status: status})
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
	cmd := &cobra.Command{
		Use:     "egress [box]",
		Short:   "Show a box's network mode, allowed destinations and recent denials",
		GroupID: "insight",
		Long: `For network = "broker": the allow-list the proxy on your Mac enforces for this
box, whether the broker is running, and the destinations it refused (names
only). A blocked install shows up here as an egress-denied line; add the host
to egress in ~/.corral/config.toml or ~/.corral/projects/<box>.toml and
run corral restart <box>.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := resolveBoxArg(args)
			if err != nil {
				return err
			}
			return printEgress(cmd.Context(), b, n)
		},
	}
	cmd.Flags().IntVar(&n, "denials", 20, "how many recent denials to show")
	return cmd
}

func printEgress(_ context.Context, b *box.Box, n int) error {
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
