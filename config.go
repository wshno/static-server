package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Config is the server's JSON configuration. Every field is optional; see defaultConfig.
type Config struct {
	// Listen is the TCP address to listen on. WEB_ADDR overrides it.
	Listen string `json:"listen"`
	// Root is the directory served at /. WEB_ROOT overrides it.
	Root string `json:"root"`
	// SPAFallback serves Root's index.html (no-cache) for a path that matches no file, so a
	// single-page app can route on the client. Paths under a mount never fall back.
	SPAFallback bool `json:"spaFallback"`
	// CacheControl is the Cache-Control for every response no rule overrides.
	CacheControl string `json:"cacheControl"`
	// Headers are set on every response, including errors (security headers, CSP).
	Headers map[string]string `json:"headers"`
	// Mounts serve other directories under a URL prefix, e.g. {"prefix": "/data/", "dir": "/run/readsb"}.
	Mounts []Mount `json:"mounts"`
	// Rules match the cleaned URL path in order; a later match overrides an earlier one field by
	// field, and their headers are merged.
	Rules []Rule `json:"rules"`
}

type Mount struct {
	Prefix string `json:"prefix"`
	Dir    string `json:"dir"`
}

type Rule struct {
	// Match is a regular expression (Go RE2 syntax) on the URL path, e.g. "^/assets/".
	Match string `json:"match"`
	// CacheControl replaces Config.CacheControl for matching paths.
	CacheControl string `json:"cacheControl"`
	// ContentEncoding marks files that are stored compressed under their plain name (readsb's
	// globe_*.json, say): the header is sent and the bytes are served as they are.
	ContentEncoding string `json:"contentEncoding"`
	// Compress gzips matching files on the fly when the client accepts gzip.
	Compress bool              `json:"compress"`
	Headers  map[string]string `json:"headers"`

	re *regexp.Regexp
}

func defaultConfig() Config {
	return Config{
		Listen:       ":8080",
		Root:         "/public",
		CacheControl: "no-cache",
		Headers: map[string]string{
			"X-Content-Type-Options": "nosniff",
			"Referrer-Policy":        "strict-origin-when-cross-origin",
		},
	}
}

// defaultConfigPath is read when it exists and neither -config nor STATIC_SERVER_CONFIG names a
// file, so an image built FROM static-server only has to COPY its config here.
const defaultConfigPath = "/etc/static-server/config.json"

// loadConfig reads the config file named by path (a missing file is an error unless path is the
// default), applies the WEB_ROOT and WEB_ADDR overrides, and validates the result.
func loadConfig(path string, explicit bool) (Config, error) {
	cfg := defaultConfig()
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		// A headers object in the file replaces the default set ({} means none) instead of merging.
		cfg.Headers = nil
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cfg); err != nil {
			return cfg, fmt.Errorf("%s: %w", path, err)
		}
		if cfg.Headers == nil {
			cfg.Headers = defaultConfig().Headers
		}
	case explicit || !errors.Is(err, os.ErrNotExist):
		return cfg, err
	}
	if v := os.Getenv("WEB_ROOT"); v != "" {
		cfg.Root = v
	}
	if v := os.Getenv("WEB_ADDR"); v != "" {
		cfg.Listen = v
	}
	return cfg, cfg.validate()
}

func (c *Config) validate() error {
	if c.Root == "" {
		return errors.New("root is empty")
	}
	for i, m := range c.Mounts {
		if !strings.HasPrefix(m.Prefix, "/") || !strings.HasSuffix(m.Prefix, "/") || m.Prefix == "/" {
			return fmt.Errorf("mounts[%d]: prefix %q must start and end with / and not be /", i, m.Prefix)
		}
		if m.Dir == "" {
			return fmt.Errorf("mounts[%d]: dir is empty", i)
		}
	}
	// Longest prefix first, so /data/globe/ wins over /data/.
	sort.SliceStable(c.Mounts, func(i, j int) bool { return len(c.Mounts[i].Prefix) > len(c.Mounts[j].Prefix) })
	for i := range c.Rules {
		r := &c.Rules[i]
		re, err := regexp.Compile(r.Match)
		if err != nil {
			return fmt.Errorf("rules[%d]: %w", i, err)
		}
		r.re = re
		switch r.ContentEncoding {
		case "", "gzip", "br", "zstd":
		default:
			return fmt.Errorf("rules[%d]: unsupported contentEncoding %q", i, r.ContentEncoding)
		}
		if r.Compress && r.ContentEncoding != "" {
			return fmt.Errorf("rules[%d]: compress and contentEncoding are mutually exclusive", i)
		}
	}
	return nil
}

// effective is the outcome of the rules for one path.
type effective struct {
	cacheControl    string
	contentEncoding string
	compress        bool
	headers         map[string]string
}

func (c *Config) resolve(upath string) effective {
	e := effective{cacheControl: c.CacheControl}
	for _, r := range c.Rules {
		if !r.re.MatchString(upath) {
			continue
		}
		if r.CacheControl != "" {
			e.cacheControl = r.CacheControl
		}
		if r.ContentEncoding != "" {
			e.contentEncoding, e.compress = r.ContentEncoding, false
		}
		if r.Compress {
			e.compress, e.contentEncoding = true, ""
		}
		for k, v := range r.Headers {
			if e.headers == nil {
				e.headers = map[string]string{}
			}
			e.headers[k] = v
		}
	}
	return e
}
