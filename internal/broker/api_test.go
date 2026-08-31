package broker

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestAPIRules(t *testing.T) {
	rules, err := ParseAPIRules([]string{"GET /api/v4/projects/42/**", "POST /api/v4/projects/42/merge_requests/*/notes", "* /version"})
	if err != nil {
		t.Fatal(err)
	}
	r := APIRoute{Allow: rules}
	yes := [][2]string{{"GET", "/api/v4/projects/42"}, {"GET", "/api/v4/projects/42/merge_requests/7/changes"}, {"POST", "/api/v4/projects/42/merge_requests/7/notes"}, {"DELETE", "/version"}, {"get", "/api/v4/projects/42/x"}}
	no := [][2]string{{"POST", "/api/v4/projects/42"}, {"GET", "/api/v4/projects/1816/x"}, {"POST", "/api/v4/projects/42/merge_requests/7/8/notes"}, {"GET", "/api/v4/projects/42/../../users"}, {"POST", "/api/v4/projects/42/merge_requests/7/notes/1"}}
	for _, c := range yes {
		if !r.Allows(c[0], c[1]) {
			t.Errorf("%s %s should be allowed", c[0], c[1])
		}
	}
	for _, c := range no {
		if r.Allows(c[0], c[1]) {
			t.Errorf("%s %s should be denied", c[0], c[1])
		}
	}
	if _, err := ParseAPIRules([]string{"GET /a/**/b"}); err == nil {
		t.Error("** in the middle must be refused")
	}
	if _, err := ParseAPIRules([]string{"GET"}); err == nil {
		t.Error("missing path must be refused")
	}
}

// An origin-form request under /<name>/ is forwarded with the credential
// added on this side; the guest's own header is dropped; denied paths and
// proxy-form requests never reach upstream.
func TestAPIRouteForwardsWithCredential(t *testing.T) {
	var got *http.Request
	var body string
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(r.Context())
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Header().Set("X-Up", "yes")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer up.Close()
	upURL, _ := url.Parse(up.URL)
	rules, _ := ParseAPIRules([]string{"GET /api/v4/projects/1/**", "POST /api/v4/projects/1/merge_requests/*/notes"})
	var audits []string
	s := &Server{
		APIs: map[string]*APIRoute{"gitlab": {Name: "gitlab", Upstream: upURL, Allow: rules, Header: "PRIVATE-TOKEN", Value: "glpat-secret"}},
		OnAPI: func(name, method, p string, status int, allowed bool) {
			audits = append(audits, name+" "+method+" "+p+" "+http.StatusText(status)+" "+map[bool]string{true: "allowed", false: "denied"}[allowed])
		},
	}
	// The test upstream has a self-signed certificate: forward through the
	// test client's transport, which trusts it.
	apiTransport = up.Client().Transport
	defer func() { apiTransport = nil }()

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), "POST", "/gitlab/api/v4/projects/1/merge_requests/7/notes?x=1", strings.NewReader(`{"body":"hi"}`))
	req.Header.Set("Authorization", "Bearer from-the-box")
	req.Header.Set("PRIVATE-TOKEN", "from-the-box")
	s.ServeHTTP(rec, req)
	if rec.Code != 201 || rec.Header().Get("X-Up") != "yes" || !strings.Contains(rec.Body.String(), "ok") {
		t.Fatalf("forward: %d %s", rec.Code, rec.Body.String())
	}
	if got == nil || got.URL.Path != "/api/v4/projects/1/merge_requests/7/notes" || got.URL.RawQuery != "x=1" || body != `{"body":"hi"}` {
		t.Errorf("upstream saw %v %q", got.URL, body)
	}
	if got.Header.Get("PRIVATE-TOKEN") != "glpat-secret" || got.Header.Get("Authorization") != "" {
		t.Errorf("credential must be the broker's, guest headers dropped: %v", got.Header)
	}

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), "DELETE", "/gitlab/api/v4/projects/1", nil))
	if rec.Code != http.StatusForbidden || rec.Header().Get("X-Corral") != "api-denied" {
		t.Errorf("denied call: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), "GET", "/jira/x", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown route is not an api call: %d", rec.Code)
	}
	if len(audits) != 2 || !strings.HasPrefix(audits[0], "gitlab POST /api/v4/projects/1/merge_requests/7/notes Created allowed") || !strings.Contains(audits[1], "denied") {
		t.Errorf("audit: %v", audits)
	}
	for _, a := range audits {
		if strings.Contains(a, "secret") || strings.Contains(a, "hi") {
			t.Errorf("audit must not carry credentials or bodies: %s", a)
		}
	}
}

func TestAPICredential(t *testing.T) {
	if h, v := APICredential("header", "PRIVATE-TOKEN", "", "t"); h != "PRIVATE-TOKEN" || v != "t" {
		t.Error(h, v)
	}
	if h, v := APICredential("bearer", "", "", "t"); h != "Authorization" || v != "Bearer t" {
		t.Error(h, v)
	}
	if h, v := APICredential("basic", "", "me@x", "t"); h != "Authorization" || v != "Basic bWVAeDp0" {
		t.Error(h, v)
	}
}
