package box

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"time"

	"github.com/corral-sh/corral/internal/broker"
	"github.com/corral-sh/corral/internal/config"
	"github.com/corral-sh/corral/internal/paths"
)

// Host-side lifecycle of a box's egress broker (network = "broker"): a child
// `corral broker --box <name>` on the Mac, tracked by PID in the metadata
// like sessions are, started by whatever starts the box and stopped by
// whatever stops it. Daemon-free, like idle_stop.

// EgressHosts is the effective allow-list: the configured egress entries plus
// every host a git token is configured for (the box must reach those to push).
func EgressHosts(cfg *config.Config) []string {
	seen := map[string]bool{}
	var out []string
	add := func(h string) {
		if h != "" && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	for _, e := range cfg.Egress {
		add(e)
	}
	hosts := make([]string, 0, len(cfg.GitTokens))
	for h := range cfg.GitTokens {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	for _, h := range hosts {
		add(h)
	}
	return out
}

// GuestGatewayIP is the Mac as seen from a Lima vz guest (user-mode network).
const GuestGatewayIP = "192.168.5.2"

// NeedsBroker reports whether the box needs the broker child on the Mac: in
// network = "broker" mode and whenever api_brokers are configured.
func NeedsBroker(cfg *config.Config) bool {
	return cfg.Network == config.NetworkBroker || len(cfg.APIBrokers) > 0
}

// APIBaseURL is where the guest reaches one api_brokers route.
func APIBaseURL(boxName, api string) string {
	return "http://" + net.JoinHostPort(GuestGatewayIP, strconv.Itoa(broker.PortFor(boxName))) + "/" + api
}

// ResolveHostVar resolves a host variable the way the launcher does for
// secrets: exported environment, then env_file, then the Keychain when the
// name is listed in keychain_env. Used by the broker child for api_brokers
// tokens; the value never leaves this process except in the upstream header.
func (b *Box) ResolveHostVar(name string) (string, error) {
	hostEnv, _, err := withEnvFile(b.Cfg, HostEnvMap(os.Environ()))
	if err != nil {
		return "", err
	}
	if v := hostEnv[name]; v != "" {
		return v, nil
	}
	for _, k := range b.Cfg.KeychainEnv {
		if k == name {
			v, err := KeychainLookup(name)
			if err == nil && v == "" {
				err = errors.New("the Keychain item is empty")
			}
			if err != nil {
				return "", fmt.Errorf("%s: %w (%s)", name, err, KeychainRemedy(name, err))
			}
			return v, nil
		}
	}
	return "", fmt.Errorf("%s is not set on the host (export it, put it in env_file, or list it in keychain_env)", name)
}

// BrokerAddr is where the box's broker listens on the Mac.
func BrokerAddr(name string) string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(broker.PortFor(name)))
}

// BrokerReady reports whether something answers on the box's broker port.
func BrokerReady(name string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	c, err := (&net.Dialer{}).DialContext(ctx, "tcp", BrokerAddr(name))
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// BrokerLog is the child's log file.
func BrokerLog(name string) (string, error) {
	dir, err := paths.LogsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "broker-"+name+".log"), nil
}

// StartBroker launches the broker child for b if none is alive, records its
// PID and waits for the port to answer. exe is the corral binary.
func (b *Box) StartBroker(ctx context.Context, exe string) error {
	if b.Meta == nil {
		return fmt.Errorf("no metadata for %s", b.Name)
	}
	if pidAlive(b.Meta.BrokerPID) && BrokerReady(b.Name) {
		return nil
	}
	if BrokerReady(b.Name) {
		return fmt.Errorf("port %s is in use by another process; the broker for %s cannot start (stop that process or pick another box name)", BrokerAddr(b.Name), b.Name)
	}
	logPath, err := BrokerLog(b.Name)
	if err != nil {
		return err
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.CommandContext(context.Background(), exe, "broker", "--box", b.Name) //nolint:gosec // our own binary
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // survives this CLI's exit and its terminal
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start egress broker: %w", err)
	}
	pid := cmd.Process.Pid // Release() resets Pid to -1; take it first
	_ = cmd.Process.Release()
	b.Meta.BrokerPID = pid
	if err := SaveMeta(b.Meta); err != nil {
		return err
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if BrokerReady(b.Name) {
			apis := make([]string, 0, len(b.Cfg.APIBrokers))
			for _, ab := range b.Cfg.APIBrokers {
				apis = append(apis, "api:"+ab.Name)
			}
			Audit(AuditEvent{Event: "broker-start", Box: b.Name, Argv: append(EgressHosts(b.Cfg), apis...)})
			return nil
		}
		if !pidAlive(b.Meta.BrokerPID) {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("egress broker for %s did not come up on %s — see %s", b.Name, BrokerAddr(b.Name), logPath)
}

// StopBroker terminates the box's broker child, if any, and clears the PID.
func StopBroker(m *Meta) {
	if m == nil || m.BrokerPID <= 0 {
		return
	}
	if pidAlive(m.BrokerPID) {
		_ = syscall.Kill(m.BrokerPID, syscall.SIGTERM)
		Audit(AuditEvent{Event: "broker-stop", Box: m.Name})
		// Wait for it: `corral stop` returning while the broker is still
		// exiting left a window in which the next start found the port busy
		// and e2e saw a stray process. SIGKILL is the fallback, not the plan.
		for i := 0; i < 40 && pidAlive(m.BrokerPID); i++ {
			time.Sleep(50 * time.Millisecond)
		}
		if pidAlive(m.BrokerPID) {
			_ = syscall.Kill(m.BrokerPID, syscall.SIGKILL)
		}
	}
	m.BrokerPID = 0
	_ = SaveMeta(m)
}

// StopBrokerFor is StopBroker for a box known only by name.
func StopBrokerFor(name string) {
	if m, err := LoadMeta(name); err == nil {
		StopBroker(m)
	}
}
