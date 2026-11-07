package main

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// fixture builds a site root, a mounted data directory, and a server over them.
func fixture(t *testing.T, mutate func(*Config)) (*server, string, string) {
	t.Helper()
	root, data := t.TempDir(), t.TempDir()
	writeFiles(t, root, map[string]string{
		"index.html":         "<html>index</html>",
		"assets/app-abc.js":  "console.log(1)",
		"docs/index.html":    "<html>docs</html>",
		"empty/.keep":        "",
		".env":               "SECRET=1",
		".git/config":        "[core]",
		".well-known/x.txt":  "well-known",
		"precompressed.json": gz(t, `{"a":1}`),
	})
	writeFiles(t, data, map[string]string{
		"aircraft.json":       `{"aircraft":[]}`,
		"globe_0001.json":     gz(t, `{"globe":1}`),
		"traces/trace_1.json": `{}`,
	})
	cfg := defaultConfig()
	cfg.Root = root
	cfg.Mounts = []Mount{{Prefix: "/data/", Dir: data}}
	if mutate != nil {
		mutate(&cfg)
	}
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	s, err := newServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, root, data
}

func gz(t *testing.T, s string) string {
	t.Helper()
	var b strings.Builder
	w := gzip.NewWriter(&b)
	if _, err := w.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func do(s http.Handler, method, target string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func TestServesFilesAndIndexes(t *testing.T) {
	s, _, _ := fixture(t, nil)
	cases := []struct {
		target string
		status int
		body   string
	}{
		{"/", 200, "<html>index</html>"},
		{"/assets/app-abc.js", 200, "console.log(1)"},
		{"/docs/", 200, "<html>docs</html>"},
		{"/.well-known/x.txt", 200, "well-known"},
		{"/data/aircraft.json", 200, `{"aircraft":[]}`},
		{"/missing.js", 404, ""},
		{"/empty/", 404, ""},             // a directory without index.html: no listing
		{"/assets/", 404, ""},            // likewise
		{"/assets/app-abc.js/", 404, ""}, // a file asked for as a directory
		{"/data/", 404, ""},
	}
	for _, c := range cases {
		rec := do(s, http.MethodGet, c.target, nil)
		if rec.Code != c.status {
			t.Errorf("%s: status %d, want %d", c.target, rec.Code, c.status)
		}
		if c.body != "" && rec.Body.String() != c.body {
			t.Errorf("%s: body %q, want %q", c.target, rec.Body.String(), c.body)
		}
	}
}

func TestDirectoryRedirect(t *testing.T) {
	s, _, _ := fixture(t, nil)
	for target, want := range map[string]string{
		"/docs":     "docs/",
		"/docs?x=1": "docs/?x=1",
		"/data":     "data/",
	} {
		rec := do(s, http.MethodGet, target, nil)
		if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != want {
			t.Errorf("%s: %d Location %q, want 301 %q", target, rec.Code, rec.Header().Get("Location"), want)
		}
	}
}

func TestTraversalStaysInside(t *testing.T) {
	s, root, _ := fixture(t, nil)
	// A file next to the root that a traversal would reach.
	outside := filepath.Join(filepath.Dir(root), "outside-secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(outside) })

	for _, target := range []string{
		"/../outside-secret.txt",
		"/%2e%2e/outside-secret.txt",
		"/assets/../../outside-secret.txt",
		"/data/../../outside-secret.txt",
		"/data/%2e%2e/%2e%2e/outside-secret.txt",
		"/..%2foutside-secret.txt",
	} {
		rec := do(s, http.MethodGet, target, nil)
		if strings.Contains(rec.Body.String(), "secret") {
			t.Errorf("%s: served a file outside the root", target)
		}
	}
}

func TestSymlinkOutOfRootIsRefused(t *testing.T) {
	s, root, _ := fixture(t, nil)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	rec := do(s, http.MethodGet, "/link.txt", nil)
	if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "secret") {
		t.Errorf("symlink out of the root: %d %q", rec.Code, rec.Body.String())
	}
}

func TestDotfilesHidden(t *testing.T) {
	s, _, _ := fixture(t, func(c *Config) { c.SPAFallback = true })
	for _, target := range []string{"/.env", "/.git/config", "/.git/"} {
		rec := do(s, http.MethodGet, target, nil)
		if strings.Contains(rec.Body.String(), "SECRET") || strings.Contains(rec.Body.String(), "[core]") {
			t.Errorf("%s: served a dotfile", target)
		}
	}
}

func TestHeadersOnEveryResponse(t *testing.T) {
	s, _, _ := fixture(t, func(c *Config) {
		c.Headers["Content-Security-Policy"] = "default-src 'self'"
	})
	for _, target := range []string{"/", "/missing", "/data/aircraft.json"} {
		rec := do(s, http.MethodGet, target, nil)
		if rec.Header().Get("Content-Security-Policy") != "default-src 'self'" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: security headers missing: %v", target, rec.Header())
		}
	}
	rec := do(s, http.MethodPost, "/", nil)
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("POST: %d %v", rec.Code, rec.Header())
	}
}

func TestRules(t *testing.T) {
	s, _, _ := fixture(t, func(c *Config) {
		c.Rules = []Rule{
			{Match: `^/assets/`, CacheControl: "public, max-age=31536000, immutable"},
			{Match: `^/data/`, Headers: map[string]string{"X-Data": "1"}},
			{Match: `^/data/aircraft\.json$`, Headers: map[string]string{"X-Data": "2"}},
			{Match: `^/docs/$`, CacheControl: "private"},
		}
	})
	if got := do(s, http.MethodGet, "/assets/app-abc.js", nil).Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("assets Cache-Control %q", got)
	}
	if got := do(s, http.MethodGet, "/", nil).Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("default Cache-Control %q", got)
	}
	if got := do(s, http.MethodGet, "/data/aircraft.json", nil).Header().Get("X-Data"); got != "2" {
		t.Errorf("later rule should win, X-Data %q", got)
	}
	if got := do(s, http.MethodGet, "/docs/", nil).Header().Get("Cache-Control"); got != "private" {
		t.Errorf("a directory index matches with its trailing slash, Cache-Control %q", got)
	}
	// A missing asset must not inherit the immutable rule.
	if got := do(s, http.MethodGet, "/assets/gone.js", nil).Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("404 Cache-Control %q", got)
	}
}

func TestSPAFallback(t *testing.T) {
	s, _, _ := fixture(t, func(c *Config) {
		c.SPAFallback = true
		c.Rules = []Rule{{Match: `^/assets/`, CacheControl: "immutable"}}
	})
	for _, target := range []string{"/projects", "/projects/42", "/assets/gone.js"} {
		rec := do(s, http.MethodGet, target, nil)
		if rec.Code != http.StatusOK || rec.Body.String() != "<html>index</html>" || rec.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: %d %q Cache-Control %q", target, rec.Code, rec.Body.String(), rec.Header().Get("Cache-Control"))
		}
	}
	// Mounts never fall back: a missing data file is a 404, not the app.
	if rec := do(s, http.MethodGet, "/data/missing.json", nil); rec.Code != http.StatusNotFound {
		t.Errorf("mount fallback: %d", rec.Code)
	}
	// Without the option, unknown paths are 404s.
	s2, _, _ := fixture(t, nil)
	if rec := do(s2, http.MethodGet, "/projects", nil); rec.Code != http.StatusNotFound {
		t.Errorf("fallback off: %d", rec.Code)
	}
}

func TestPrecompressed(t *testing.T) {
	s, _, _ := fixture(t, func(c *Config) {
		c.Rules = []Rule{{Match: `/globe_[0-9]+\.json$`, ContentEncoding: "gzip"}}
	})
	rec := do(s, http.MethodGet, "/data/globe_0001.json", map[string]string{"Accept-Encoding": "gzip"})
	if rec.Header().Get("Content-Encoding") != "gzip" || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("headers %v", rec.Header())
	}
	if got := gunzip(t, rec.Body.Bytes()); got != `{"globe":1}` {
		t.Errorf("body %q", got)
	}
}

func TestCompressOnTheFly(t *testing.T) {
	s, _, _ := fixture(t, func(c *Config) {
		c.Rules = []Rule{{Match: `^/data/.*\.json$`, Compress: true}}
	})
	rec := do(s, http.MethodGet, "/data/aircraft.json", map[string]string{"Accept-Encoding": "br, gzip;q=0.8", "Range": "bytes=0-3"})
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "gzip" || rec.Header().Get("Content-Length") != "" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	if rec.Header().Get("Vary") != "Accept-Encoding" {
		t.Errorf("Vary %q", rec.Header().Get("Vary"))
	}
	if got := gunzip(t, rec.Body.Bytes()); got != `{"aircraft":[]}` {
		t.Errorf("body %q", got)
	}

	// No gzip in Accept-Encoding (or q=0): plain, but still Vary.
	for _, ae := range []string{"", "br", "gzip;q=0"} {
		rec = do(s, http.MethodGet, "/data/aircraft.json", map[string]string{"Accept-Encoding": ae})
		if rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != `{"aircraft":[]}` || rec.Header().Get("Vary") != "Accept-Encoding" {
			t.Errorf("Accept-Encoding %q: %v %q", ae, rec.Header(), rec.Body.String())
		}
	}

	// HEAD and 304 have no body, so no gzip stream either.
	rec = do(s, http.MethodHead, "/data/aircraft.json", map[string]string{"Accept-Encoding": "gzip"})
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("HEAD: %d %d bytes", rec.Code, rec.Body.Len())
	}
	lm := rec.Header().Get("Last-Modified")
	rec = do(s, http.MethodGet, "/data/aircraft.json", map[string]string{"Accept-Encoding": "gzip", "If-Modified-Since": lm})
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Errorf("conditional GET: %d %d bytes", rec.Code, rec.Body.Len())
	}
}

func gunzip(t *testing.T, b []byte) string {
	t.Helper()
	r, err := gzip.NewReader(strings.NewReader(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
