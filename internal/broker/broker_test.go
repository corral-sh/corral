package broker

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestAllows(t *testing.T) {
	a, err := Parse([]string{"api.anthropic.com", "*.npmjs.org", "git.example.com:8443", "Trailing.Dot."})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		host string
		port int
		want bool
	}{
		{"api.anthropic.com", 443, true},
		{"API.Anthropic.COM", 80, true},
		{"api.anthropic.com", 8080, false}, // no port given → 80/443 only
		{"evil-api.anthropic.com", 443, false},
		{"registry.npmjs.org", 443, true},
		{"a.b.npmjs.org", 443, true},
		{"npmjs.org", 443, false}, // wildcard needs a label
		{"notnpmjs.org", 443, false},
		{"git.example.com", 8443, true},
		{"git.example.com", 443, false},
		{"trailing.dot", 443, true},
		{"93.184.216.34", 443, false}, // IP literals never match
		{"", 443, false},
	}
	for _, c := range cases {
		if got := a.Allows(c.host, c.port); got != c.want {
			t.Errorf("Allows(%q,%d) = %v, want %v", c.host, c.port, got, c.want)
		}
	}
	if _, err := Parse([]string{"host:99999"}); err == nil {
		t.Error("bad port accepted")
	}
}

func TestPortForIsStableAndInRange(t *testing.T) {
	a, b := PortFor("inspect-api-3f9a2c"), PortFor("inspect-api-3f9a2c")
	if a != b || a < PortBase || a >= PortBase+PortRange {
		t.Fatalf("port %d / %d", a, b)
	}
	if PortFor("other-box") == a && PortFor("third") == a {
		t.Fatal("suspiciously constant")
	}
}

func TestRefusesNonLoopbackBind(t *testing.T) {
	s := &Server{}
	if err := s.ListenAndServe(context.Background(), "0.0.0.0:0"); err == nil {
		t.Fatal("bound a non-loopback address")
	}
}

// Live: an allowed CONNECT tunnels TLS end-to-end; a denied one gets 403 and
// fires the audit hook; plain HTTP forwards; the upstream is never dialled on
// a denial.
func TestProxyEndToEnd(t *testing.T) {
	tlsUp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("tls-ok")) }))
	defer tlsUp.Close()
	plainUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("plain-ok " + r.URL.Path)) }))
	defer plainUp.Close()

	var denied []string
	dialed := map[string]bool{}
	allow, _ := Parse([]string{"allowed.test", "plain.test:" + port(plainUp.URL)})
	s := &Server{
		Allow:  allow,
		OnDeny: func(h string, p int) { denied = append(denied, h) },
		// Names are fake: map them onto the local upstreams.
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialed[addr] = true
			d := &net.Dialer{}
			switch {
			case strings.HasPrefix(addr, "allowed.test:"):
				return d.DialContext(ctx, "tcp", strings.TrimPrefix(tlsUp.URL, "https://"))
			case strings.HasPrefix(addr, "plain.test:"):
				return d.DialContext(ctx, "tcp", strings.TrimPrefix(plainUp.URL, "http://"))
			}
			return nil, net.UnknownNetworkError(addr)
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve(ctx, ln) }()
	proxyURL, _ := url.Parse("http://" + ln.Addr().String())
	client := &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test server cert
	}}
	get := func(u string) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		return client.Do(req)
	}

	resp, err := get("https://allowed.test/")
	if err != nil {
		t.Fatal(err)
	}
	if body := read(resp); body != "tls-ok" {
		t.Fatalf("allowed CONNECT body %q", body)
	}
	// Go's client turns a non-200 CONNECT reply into an error carrying the status.
	resp, err = get("https://denied.test/")
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "Forbidden") || len(denied) != 1 || denied[0] != "denied.test" {
		t.Fatalf("denied: err %v, hook %v", err, denied)
	}
	if dialed["denied.test:443"] {
		t.Fatal("upstream dialled for a denied host")
	}
	resp, err = get("http://plain.test:" + port(plainUp.URL) + "/x")
	if err != nil {
		t.Fatal(err)
	}
	if body := read(resp); body != "plain-ok /x" {
		t.Fatalf("plain forward body %q (status %d)", body, resp.StatusCode)
	}
	if a, d := s.Stats.Snapshot(); a != 2 || d != 1 {
		t.Fatalf("stats allowed=%d denied=%d", a, d)
	}
}

func read(r *http.Response) string {
	defer r.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 1024)
	for {
		n, err := r.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return sb.String()
}

func port(u string) string {
	_, p, _ := net.SplitHostPort(strings.TrimPrefix(strings.TrimPrefix(u, "http://"), "https://"))
	return p
}
