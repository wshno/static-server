package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadConfig(t *testing.T) {
	p := writeConfig(t, `{
		"root": "/srv",
		"spaFallback": true,
		"headers": {"Content-Security-Policy": "default-src 'self'"},
		"mounts": [{"prefix": "/data/", "dir": "/run/a"}, {"prefix": "/data/globe/", "dir": "/run/b"}],
		"rules": [{"match": "^/assets/", "cacheControl": "immutable"}]
	}`)
	cfg, err := loadConfig(p, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Root != "/srv" || cfg.Listen != ":8080" || !cfg.SPAFallback || cfg.CacheControl != "no-cache" {
		t.Errorf("config %+v", cfg)
	}
	// Headers replace the default set rather than merging into it.
	if len(cfg.Headers) != 1 {
		t.Errorf("headers %v", cfg.Headers)
	}
	if cfg.Mounts[0].Prefix != "/data/globe/" {
		t.Errorf("mounts should be longest prefix first: %v", cfg.Mounts)
	}
}

func TestLoadConfigEnvOverrides(t *testing.T) {
	t.Setenv("WEB_ROOT", "/override")
	t.Setenv("WEB_ADDR", ":9000")
	cfg, err := loadConfig(writeConfig(t, `{"root": "/srv"}`), true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Root != "/override" || cfg.Listen != ":9000" {
		t.Errorf("config %+v", cfg)
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.json")
	if _, err := loadConfig(missing, false); err != nil {
		t.Errorf("a missing default config should fall back to defaults: %v", err)
	}
	if _, err := loadConfig(missing, true); err == nil {
		t.Error("a missing explicit config should be an error")
	}
}

func TestLoadConfigErrors(t *testing.T) {
	for body, want := range map[string]string{
		`{"spaFalback": true}`:                                                     "unknown field",
		`{"mounts": [{"prefix": "/data", "dir": "/x"}]}`:                           "must start and end with /",
		`{"mounts": [{"prefix": "/", "dir": "/x"}]}`:                               "must start and end with /",
		`{"mounts": [{"prefix": "/data/"}]}`:                                       "dir is empty",
		`{"rules": [{"match": "("}]}`:                                              "rules[0]",
		`{"rules": [{"match": ".", "contentEncoding": "lzma"}]}`:                   "unsupported contentEncoding",
		`{"rules": [{"match": ".", "contentEncoding": "gzip", "compress": true}]}`: "mutually exclusive",
		`{"root": ""}`: "root is empty",
	} {
		_, err := loadConfig(writeConfig(t, body), true)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error %v, want %q", body, err, want)
		}
	}
}
