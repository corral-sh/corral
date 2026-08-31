// Package broker is the egress allow-list proxy that runs on the Mac for a
// box with network = "broker". It is a plain HTTP CONNECT / forward proxy:
// it never terminates TLS, never caches, and decides on the destination host
// alone. The guest is funnelled to it by nftables (see scripts/broker.sh);
// this package only answers "may this box talk to that host?" — outside the
// boundary, so the agent cannot change the answer.
package broker

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PortBase..PortBase+PortRange is where per-box brokers listen on loopback.
const (
	PortBase  = 42000
	PortRange = 1000
)

// PortFor derives the deterministic broker port of a box from its name, so
// the port can be rendered into the guest template without changing per start.
func PortFor(box string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(box))
	return PortBase + int(h.Sum32()%PortRange)
}

// Rule is one allow-list entry: exact host or "*.suffix", optional port
// (0 = the default ports 80 and 443).
type Rule struct {
	Host     string
	Wildcard bool // Host is a suffix: "*.example.com" matches a.example.com, not example.com itself
	Port     int
}

// AllowList is a parsed egress list.
type AllowList []Rule

// Parse turns config egress entries into rules. Entries were already shape-
// validated by config.Resolve; this is lenient on case and trailing dots.
func Parse(entries []string) (AllowList, error) {
	var out AllowList
	for _, e := range entries {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		r := Rule{}
		host, port, err := splitHostPort(e)
		if err != nil {
			return nil, fmt.Errorf("egress %q: %w", e, err)
		}
		r.Port = port
		if strings.HasPrefix(host, "*.") {
			r.Wildcard = true
			host = strings.TrimPrefix(host, "*.")
		}
		r.Host = strings.TrimSuffix(host, ".")
		if r.Host == "" {
			return nil, fmt.Errorf("egress %q: empty host", e)
		}
		out = append(out, r)
	}
	return out, nil
}

func splitHostPort(s string) (string, int, error) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return s, 0, nil
	}
	p, err := strconv.Atoi(s[i+1:])
	if err != nil || p < 1 || p > 65535 {
		return "", 0, errors.New("bad port")
	}
	return s[:i], p, nil
}

// Allows reports whether host:port may be reached.
func (a AllowList) Allows(host string, port int) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" || net.ParseIP(host) != nil {
		return false // names only: an IP literal sidesteps the whole point of a list
	}
	for _, r := range a {
		if r.Port != 0 {
			if r.Port != port {
				continue
			}
		} else if port != 80 && port != 443 {
			continue
		}
		if r.Wildcard {
			if strings.HasSuffix(host, "."+r.Host) {
				return true
			}
		} else if host == r.Host {
			return true
		}
	}
	return false
}

// Stats counts what the broker did; read by `corral egress`.
type Stats struct {
	mu      sync.Mutex
	Allowed int
	Denied  int
}

func (s *Stats) add(allowed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if allowed {
		s.Allowed++
	} else {
		s.Denied++
	}
}

// Snapshot returns the counters.
func (s *Stats) Snapshot() (allowed, denied int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Allowed, s.Denied
}

// Server is one box's broker: the egress allow-list proxy and, when
// configured, the credential-holding API routes (api.go).
type Server struct {
	Allow  AllowList
	OnDeny func(host string, port int) // audit hook; names only
	// APIs by name; OnAPI is the audit hook for every API call (method, path,
	// status, whether the allow-list let it through) — never bodies.
	APIs  map[string]*APIRoute
	OnAPI func(name, method, path string, status int, allowed bool)
	Stats Stats
	// Dial is how upstream connections are made (tests swap it).
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// ListenAndServe serves on addr (loopback only) until ctx is done.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("broker must bind a loopback address, not %q", addr)
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	return s.Serve(ctx, ln)
}

// Serve serves on an existing listener until ctx is done.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	if s.Dial == nil {
		s.Dial = (&net.Dialer{Timeout: 15 * time.Second}).DialContext
	}
	srv := &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	go func() {
		<-ctx.Done()
		// ctx is already done; give in-flight requests a bounded grace period.
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	err := srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// ServeHTTP handles CONNECT (TLS tunnels) and plain absolute-URI requests.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		s.connect(w, r)
		return
	}
	if route, rest, ok := s.apiRoute(r); ok {
		s.api(w, r, route, rest)
		return
	}
	if !r.URL.IsAbs() {
		http.Error(w, "corral broker: proxy requests only (api_brokers routes are /<name>/…)", http.StatusBadRequest)
		return
	}
	host, port := hostPort(r.URL.Host, 80)
	if !s.decide(host, port) {
		s.deny(w, host, port)
		return
	}
	s.forward(w, r)
}

func (s *Server) decide(host string, port int) bool {
	ok := s.Allow.Allows(host, port)
	s.Stats.add(ok)
	if !ok && s.OnDeny != nil {
		s.OnDeny(host, port)
	}
	return ok
}

func (s *Server) deny(w http.ResponseWriter, host string, port int) {
	w.Header().Set("X-Corral", "egress-denied")
	http.Error(w, fmt.Sprintf("corral: egress to %s:%d is not in this box's allow-list (see `corral egress`)", host, port), http.StatusForbidden)
}

func (s *Server) connect(w http.ResponseWriter, r *http.Request) {
	host, port := hostPort(r.Host, 443)
	if !s.decide(host, port) {
		s.deny(w, host, port)
		return
	}
	upstream, err := s.Dial(r.Context(), "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		http.Error(w, "corral broker: "+err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	_, _ = buf.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
	_ = buf.Flush()
	pipe(client, upstream)
}

func (s *Server) forward(w http.ResponseWriter, r *http.Request) {
	tr := &http.Transport{DialContext: s.Dial, Proxy: nil, DisableCompression: true}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	for _, h := range []string{"Proxy-Connection", "Proxy-Authorization", "Connection"} {
		out.Header.Del(h)
	}
	resp, err := tr.RoundTrip(out)
	if err != nil {
		http.Error(w, "corral broker: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func pipe(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if t, ok := dst.(*net.TCPConn); ok {
			_ = t.CloseWrite()
		}
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
	_ = a.Close()
	_ = b.Close()
}

func hostPort(hp string, def int) (string, int) {
	host, p, err := net.SplitHostPort(hp)
	if err != nil {
		return strings.ToLower(hp), def
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		n = def
	}
	return strings.ToLower(host), n
}
