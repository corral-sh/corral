package broker

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// APIRoute is one credential-holding proxy: requests the guest sends to
// http://<gateway>:<port>/<Name>/rest are forwarded to Upstream/rest with the
// credential added here, on the Mac. The token never enters the box; the box
// gets exactly the calls Allow lists.
type APIRoute struct {
	Name     string
	Upstream *url.URL // https://host[:port], no path
	Allow    []APIRule
	// Credential: Header/Value are set on every forwarded request (the guest's
	// own Authorization / same-named header is dropped first).
	Header string
	Value  string
}

// APIRule is one "METHOD /path" allow entry. Method "*" matches any; the path
// pattern is matched segment by segment: "*" is one segment, "**" the rest.
type APIRule struct {
	Method string
	Path   []string
}

// ParseAPIRules parses config allow entries ("GET /api/v4/projects/42/**").
func ParseAPIRules(entries []string) ([]APIRule, error) {
	out := make([]APIRule, 0, len(entries))
	for _, e := range entries {
		method, p, ok := strings.Cut(strings.TrimSpace(e), " ")
		if !ok || !strings.HasPrefix(p, "/") {
			return nil, fmt.Errorf("api allow %q: want \"METHOD /path\"", e)
		}
		segs := strings.Split(strings.Trim(path.Clean(p), "/"), "/")
		for i, s := range segs {
			if s == "**" && i != len(segs)-1 {
				return nil, fmt.Errorf("api allow %q: ** may only end the pattern", e)
			}
		}
		out = append(out, APIRule{Method: strings.ToUpper(method), Path: segs})
	}
	return out, nil
}

// Allows reports whether method + request path match one rule.
func (r APIRoute) Allows(method, reqPath string) bool {
	segs := strings.Split(strings.Trim(path.Clean("/"+reqPath), "/"), "/")
	for _, rule := range r.Allow {
		if rule.Method != "*" && rule.Method != strings.ToUpper(method) {
			continue
		}
		if matchSegments(rule.Path, segs) {
			return true
		}
	}
	return false
}

func matchSegments(pattern, segs []string) bool {
	for i, p := range pattern {
		if p == "**" {
			return true
		}
		if i >= len(segs) {
			return false
		}
		if p != "*" && p != segs[i] {
			return false
		}
	}
	return len(pattern) == len(segs)
}

// APICredential renders the header for a config auth style.
func APICredential(auth, header, user, token string) (name, value string) {
	switch auth {
	case "bearer":
		return "Authorization", "Bearer " + token
	case "basic":
		return "Authorization", "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+token))
	default:
		return header, token
	}
}

// apiRoute picks the API route an origin-form request addresses, by first
// path segment. Proxy-form (absolute URI) requests are never API calls: those
// are egress to the internet and stay with the allow-list.
func (s *Server) apiRoute(r *http.Request) (*APIRoute, string, bool) {
	if r.URL.IsAbs() || len(s.APIs) == 0 {
		return nil, "", false
	}
	p := strings.TrimPrefix(r.URL.Path, "/")
	name, rest, _ := strings.Cut(p, "/")
	route, ok := s.APIs[name]
	if !ok {
		return nil, "", false
	}
	return route, "/" + rest, true
}

// apiTransport overrides the upstream transport (tests: a self-signed upstream).
var apiTransport http.RoundTripper

// hopHeaders are never forwarded in either direction.
var hopHeaders = []string{"Connection", "Proxy-Connection", "Proxy-Authorization", "Keep-Alive", "Transfer-Encoding", "Upgrade", "Te", "Trailer"}

func (s *Server) api(w http.ResponseWriter, r *http.Request, route *APIRoute, rest string) {
	if !route.Allows(r.Method, rest) {
		s.Stats.add(false)
		if s.OnAPI != nil {
			s.OnAPI(route.Name, r.Method, rest, http.StatusForbidden, false)
		}
		w.Header().Set("X-Corral", "api-denied")
		http.Error(w, fmt.Sprintf("corral: %s %s is not in the %s api_broker allow-list (see `corral egress`)", r.Method, rest, route.Name), http.StatusForbidden)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.URL = &url.URL{Scheme: route.Upstream.Scheme, Host: route.Upstream.Host, Path: rest, RawQuery: r.URL.RawQuery}
	out.Host = route.Upstream.Host
	for _, h := range hopHeaders {
		out.Header.Del(h)
	}
	// The box never supplies the credential — whatever it sent is dropped.
	out.Header.Del("Authorization")
	out.Header.Del(route.Header)
	out.Header.Set(route.Header, route.Value)
	out.Header.Set("X-Forwarded-By", "corral")
	var tr http.RoundTripper = &http.Transport{DialContext: s.Dial, Proxy: nil, DisableCompression: true}
	if apiTransport != nil {
		tr = apiTransport
	}
	resp, err := tr.RoundTrip(out)
	if err != nil {
		s.Stats.add(false)
		if s.OnAPI != nil {
			s.OnAPI(route.Name, r.Method, rest, http.StatusBadGateway, true)
		}
		http.Error(w, "corral api broker: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	s.Stats.add(true)
	if s.OnAPI != nil {
		s.OnAPI(route.Name, r.Method, rest, resp.StatusCode, true)
	}
	for k, vv := range resp.Header {
		if isHop(k) {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func isHop(h string) bool {
	for _, x := range hopHeaders {
		if strings.EqualFold(x, h) {
			return true
		}
	}
	return false
}
