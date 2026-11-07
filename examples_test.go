package main

import (
	"path/filepath"
	"testing"
)

// The example configs load, and the tar1090 one reproduces tar1090's nginx.conf headers.
func TestExamples(t *testing.T) {
	for _, name := range []string{"spa.json", "tar1090.json"} {
		if _, err := loadConfig(filepath.Join("examples", name), true); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	cfg, err := loadConfig(filepath.Join("examples", "tar1090.json"), true)
	if err != nil {
		t.Fatal(err)
	}
	for upath, want := range map[string]effective{
		"/":                                      {cacheControl: "no-cache"},
		"/config.js":                             {cacheControl: "no-cache"},
		"/script_abc.js":                         {cacheControl: "public, max-age=1209600"},
		"/db-2ab834a/aircraft.js":                {cacheControl: "public, max-age=1209600", contentEncoding: "gzip"},
		"/data/aircraft.json":                    {cacheControl: "no-cache", compress: true},
		"/data/globe_0001.json":                  {cacheControl: "no-cache", contentEncoding: "gzip"},
		"/data/traces/ab/trace_full_abcdef.json": {cacheControl: "no-cache", contentEncoding: "gzip"},
		"/data/data.binCraft":                    {cacheControl: "no-cache", contentEncoding: "gzip"},
	} {
		got := cfg.resolve(upath)
		if got.cacheControl != want.cacheControl || got.contentEncoding != want.contentEncoding || got.compress != want.compress {
			t.Errorf("%s: %+v, want %+v", upath, got, want)
		}
	}
}
