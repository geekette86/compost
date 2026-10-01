package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadFileAndEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compost.json")
	os.WriteFile(path, []byte(`{
		"listen": ":9000",
		"mirrors": [{"name": "packagist", "upstream": "https://repo.packagist.org/", "ttl": "5m"},
		            {"name": "drupal", "upstream": "https://packages.drupal.org/", "ttl": 120}]
	}`), 0o644)
	t.Setenv("COMPOST_CACHE_DIR", "/var/cache/compost")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":9000" || cfg.CacheDir != "/var/cache/compost" || len(cfg.Mirrors) != 2 {
		t.Fatalf("cfg = %+v", cfg)
	}
	if cfg.Mirrors[0].TTL != Duration(5*time.Minute) || cfg.Mirrors[1].TTL != Duration(2*time.Minute) {
		t.Fatalf("ttls = %v, %v", cfg.Mirrors[0].TTL, cfg.Mirrors[1].TTL)
	}
}

func TestValidateRejects(t *testing.T) {
	for name, m := range map[string]Mirror{
		"bad name":       {Name: "Bad Name", Upstream: "https://x/"},
		"reserved name":  {Name: "stats", Upstream: "https://x/"},
		"no slash":       {Name: "x", Upstream: "https://x/api"},
		"relative":       {Name: "x", Upstream: "/api/"},
		"bad regex":      {Name: "x", Upstream: "https://x/", AllowedPaths: []string{"("}},
		"negative ttl":   {Name: "x", Upstream: "https://x/", TTL: -1},
		"query upstream": {Name: "x", Upstream: "https://x/?a=b"},
	} {
		cfg := Default()
		cfg.Mirrors = []Mirror{m}
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestExpandHeaders(t *testing.T) {
	env := map[string]string{"TOKEN": "abc"}
	got := ExpandHeaders(map[string]string{
		"Authorization": "Bearer ${TOKEN}",
		"X-Missing":     "${MISSING}",
		"X-Plain":       "plain",
	}, func(k string) string { return env[k] })
	if got["Authorization"] != "Bearer abc" || got["X-Plain"] != "plain" {
		t.Fatalf("got %v", got)
	}
	if _, ok := got["X-Missing"]; ok {
		t.Fatal("header with missing variable kept")
	}
}
