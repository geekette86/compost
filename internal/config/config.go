// Package config holds the Compost proxy configuration: where to listen,
// where to keep the heap (cache), and which upstreams to mirror.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// Duration is a time.Duration that marshals to and from JSON as a Go duration
// string ("90s", "10m", "24h"). Plain numbers are read as seconds.
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		v, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", s, err)
		}
		*d = Duration(v)
		return nil
	}
	var secs float64
	if err := json.Unmarshal(b, &secs); err != nil {
		return fmt.Errorf("duration must be a string like \"10m\" or a number of seconds")
	}
	*d = Duration(secs * float64(time.Second))
	return nil
}

// Mirror describes one upstream that Compost caches.
type Mirror struct {
	// Name is the URL path segment the mirror is served under: /<name>/...
	Name string `json:"name"`
	// Upstream is the base URL being mirrored. It must end with a slash.
	Upstream string `json:"upstream"`
	// TTL is how long a cached object is served before it is revalidated
	// upstream. Zero means objects never expire (right for immutable dists).
	TTL Duration `json:"ttl"`
	// AllowedPaths are regular expressions matched against the path below the
	// upstream. When set, anything else is refused, which keeps Compost from
	// becoming an open proxy for e.g. the GitHub API with your token attached.
	AllowedPaths []string `json:"allowed_paths,omitempty"`
	// Headers are sent upstream. Values may reference environment variables as
	// ${VAR}; a header whose variable is unset or empty is dropped.
	Headers map[string]string `json:"headers,omitempty"`
}

// Config is the full proxy configuration.
type Config struct {
	Listen          string   `json:"listen"`
	CacheDir        string   `json:"cache_dir"`
	UpstreamTimeout Duration `json:"upstream_timeout"`
	// MaxObjectSize caps a single cached object in bytes. Zero means no limit.
	MaxObjectSize int64    `json:"max_object_size"`
	Mirrors       []Mirror `json:"mirrors"`
}

// Default returns the built-in configuration: Packagist metadata plus the
// GitHub hosts most Packagist dists are downloaded from.
func Default() Config {
	githubAuth := map[string]string{"Authorization": "Bearer ${GITHUB_TOKEN}"}
	return Config{
		Listen:          ":8080",
		CacheDir:        "./heap",
		UpstreamTimeout: Duration(5 * time.Minute),
		MaxObjectSize:   1 << 30,
		Mirrors: []Mirror{
			{
				Name:     "packagist",
				Upstream: "https://repo.packagist.org/",
				TTL:      Duration(time.Minute),
			},
			{
				Name:         "github-api",
				Upstream:     "https://api.github.com/repos/",
				AllowedPaths: []string{`^[^/]+/[^/]+/(zipball|tarball)/[^/]+$`},
				Headers:      githubAuth,
			},
			{
				Name:         "github-codeload",
				Upstream:     "https://codeload.github.com/",
				AllowedPaths: []string{`^[^/]+/[^/]+/(legacy\.zip|legacy\.tar\.gz|zip|tar\.gz)/.+$`},
			},
			{
				Name:         "gitlab",
				Upstream:     "https://gitlab.com/api/v4/projects/",
				AllowedPaths: []string{`^[^/]+/repository/archive\.(zip|tar\.gz)$`},
			},
			{
				Name:         "bitbucket",
				Upstream:     "https://bitbucket.org/",
				AllowedPaths: []string{`^[^/]+/[^/]+/get/[^/]+\.(zip|tar\.gz)$`},
			},
		},
	}
}

// Load reads the configuration. With an empty path the defaults are used.
// COMPOST_LISTEN and COMPOST_CACHE_DIR override the file in both cases.
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("reading config: %w", err)
		}
		// Unmarshal over the defaults so a file may set only what it changes;
		// a "mirrors" key replaces the default mirror list as a whole.
		if err := json.Unmarshal(b, &cfg); err != nil {
			return Config{}, fmt.Errorf("parsing config %s: %w", path, err)
		}
	}
	if v := os.Getenv("COMPOST_LISTEN"); v != "" {
		cfg.Listen = v
	}
	if v := os.Getenv("COMPOST_CACHE_DIR"); v != "" {
		cfg.CacheDir = v
	}
	return cfg, cfg.Validate()
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// reservedNames collide with Compost's own endpoints.
var reservedNames = map[string]bool{"mirrors.json": true, "healthz": true, "stats": true}

// Validate reports the first problem found in the configuration.
func (c Config) Validate() error {
	if c.CacheDir == "" {
		return errors.New("cache_dir must not be empty")
	}
	if len(c.Mirrors) == 0 {
		return errors.New("at least one mirror is required")
	}
	seen := map[string]bool{}
	for i, m := range c.Mirrors {
		if !nameRE.MatchString(m.Name) || reservedNames[m.Name] {
			return fmt.Errorf("mirror %d: invalid name %q (lowercase letters, digits and dashes)", i, m.Name)
		}
		if seen[m.Name] {
			return fmt.Errorf("mirror %q: duplicate name", m.Name)
		}
		seen[m.Name] = true
		u, err := url.Parse(m.Upstream)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("mirror %q: upstream must be an absolute http(s) URL", m.Name)
		}
		if !strings.HasSuffix(u.Path, "/") || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("mirror %q: upstream must end with a slash and have no query", m.Name)
		}
		if m.TTL < 0 {
			return fmt.Errorf("mirror %q: ttl must not be negative", m.Name)
		}
		for _, p := range m.AllowedPaths {
			if _, err := regexp.Compile(p); err != nil {
				return fmt.Errorf("mirror %q: allowed_paths: %w", m.Name, err)
			}
		}
	}
	return nil
}

var envRefRE = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// ExpandHeaders resolves ${VAR} references in header values using lookup.
// Headers that reference a missing or empty variable are omitted, so an
// unset GITHUB_TOKEN means "no Authorization header", not "Bearer ".
func ExpandHeaders(h map[string]string, lookup func(string) string) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		missing := false
		expanded := envRefRE.ReplaceAllStringFunc(v, func(ref string) string {
			val := lookup(envRefRE.FindStringSubmatch(ref)[1])
			if val == "" {
				missing = true
			}
			return val
		})
		if !missing {
			out[k] = expanded
		}
	}
	return out
}
