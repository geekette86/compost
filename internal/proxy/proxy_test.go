package proxy

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/geekette86/compost/internal/cache"
	"github.com/geekette86/compost/internal/config"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type fixture struct {
	srv      *httptest.Server
	upstream *httptest.Server
	clock    *clock
	calls    atomic.Int64
	handler  http.HandlerFunc
	lastReq  atomic.Pointer[http.Request]
}

func newFixture(t *testing.T, mirrors func(upstream string) []config.Mirror, env map[string]string) *fixture {
	t.Helper()
	f := &fixture{clock: &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}}
	f.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		f.lastReq.Store(r)
		f.handler(w, r)
	}))
	t.Cleanup(f.upstream.Close)

	cfg := config.Default()
	cfg.CacheDir = t.TempDir()
	cfg.Mirrors = mirrors(f.upstream.URL + "/")
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	store, err := cache.New(cfg.CacheDir, 1024)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{
		Config: cfg,
		Store:  store,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Getenv: func(k string) string { return env[k] },
		Now:    f.clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.srv = httptest.NewServer(s)
	t.Cleanup(f.srv.Close)
	return f
}

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func oneMirror(ttl time.Duration) func(string) []config.Mirror {
	return func(u string) []config.Mirror {
		return []config.Mirror{{Name: "pkg", Upstream: u, TTL: config.Duration(ttl)}}
	}
}

func TestMissThenHit(t *testing.T) {
	f := newFixture(t, oneMirror(0), nil)
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		io.WriteString(w, "zip:"+r.URL.Path)
	}

	resp, body := get(t, f.srv.URL+"/pkg/a/b.zip")
	if resp.StatusCode != 200 || body != "zip:/a/b.zip" || resp.Header.Get("X-Compost-Cache") != "MISS" {
		t.Fatalf("first request: %d %q %q", resp.StatusCode, body, resp.Header.Get("X-Compost-Cache"))
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/zip" {
		t.Errorf("content type %q", ct)
	}

	f.clock.Advance(365 * 24 * time.Hour) // TTL 0 never expires
	resp, body = get(t, f.srv.URL+"/pkg/a/b.zip")
	if body != "zip:/a/b.zip" || resp.Header.Get("X-Compost-Cache") != "HIT" {
		t.Fatalf("second request: %q %q", body, resp.Header.Get("X-Compost-Cache"))
	}
	if n := f.calls.Load(); n != 1 {
		t.Errorf("upstream calls = %d, want 1", n)
	}
}

func TestRevalidation(t *testing.T) {
	f := newFixture(t, oneMirror(time.Minute), nil)
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		io.WriteString(w, `{"packages":{}}`)
	}

	get(t, f.srv.URL+"/pkg/p2/a/b.json")
	f.clock.Advance(30 * time.Second)
	if resp, _ := get(t, f.srv.URL+"/pkg/p2/a/b.json"); resp.Header.Get("X-Compost-Cache") != "HIT" {
		t.Fatalf("within TTL: %s", resp.Header.Get("X-Compost-Cache"))
	}
	f.clock.Advance(time.Minute)
	resp, body := get(t, f.srv.URL+"/pkg/p2/a/b.json")
	if resp.Header.Get("X-Compost-Cache") != "REVALIDATED" || body != `{"packages":{}}` {
		t.Fatalf("after TTL: %s %q", resp.Header.Get("X-Compost-Cache"), body)
	}
	if resp, _ := get(t, f.srv.URL+"/pkg/p2/a/b.json"); resp.Header.Get("X-Compost-Cache") != "HIT" {
		t.Fatalf("after revalidation: %s", resp.Header.Get("X-Compost-Cache"))
	}
	if n := f.calls.Load(); n != 2 {
		t.Errorf("upstream calls = %d, want 2", n)
	}
}

func TestStaleOnUpstreamFailure(t *testing.T) {
	f := newFixture(t, oneMirror(time.Minute), nil)
	var broken atomic.Bool
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		if broken.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		io.WriteString(w, "v1")
	}
	get(t, f.srv.URL+"/pkg/x.json")
	broken.Store(true)
	f.clock.Advance(2 * time.Minute)
	resp, body := get(t, f.srv.URL+"/pkg/x.json")
	if resp.StatusCode != 200 || body != "v1" || resp.Header.Get("X-Compost-Cache") != "STALE" {
		t.Fatalf("got %d %q %s", resp.StatusCode, body, resp.Header.Get("X-Compost-Cache"))
	}
	if resp, _ := get(t, f.srv.URL+"/pkg/never-cached.json"); resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("uncached failure: %d", resp.StatusCode)
	}
}

func TestNotFoundIsPassedThroughAndNotCached(t *testing.T) {
	f := newFixture(t, oneMirror(0), nil)
	f.handler = func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }
	for i := 0; i < 2; i++ {
		if resp, _ := get(t, f.srv.URL+"/pkg/missing.zip"); resp.StatusCode != 404 {
			t.Fatalf("status %d", resp.StatusCode)
		}
	}
	if n := f.calls.Load(); n != 2 {
		t.Errorf("upstream calls = %d, want 2", n)
	}
}

func TestAllowedPathsAndHeaders(t *testing.T) {
	f := newFixture(t, func(u string) []config.Mirror {
		return []config.Mirror{{
			Name:         "gh",
			Upstream:     u,
			AllowedPaths: []string{`^[^/]+/[^/]+/zipball/[^/]+$`},
			Headers:      map[string]string{"Authorization": "Bearer ${TOKEN}", "X-Unset": "${NOPE}"},
		}}
	}, map[string]string{"TOKEN": "s3cret"})
	f.handler = func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "zip") }

	if resp, _ := get(t, f.srv.URL+"/gh/user"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("disallowed path: %d", resp.StatusCode)
	}
	if resp, _ := get(t, f.srv.URL+"/gh/acme/lib/zipball/abc123"); resp.StatusCode != 200 {
		t.Fatalf("allowed path: %d", resp.StatusCode)
	}
	r := f.lastReq.Load()
	if got := r.Header.Get("Authorization"); got != "Bearer s3cret" {
		t.Errorf("Authorization = %q", got)
	}
	if _, ok := r.Header["X-Unset"]; ok {
		t.Error("header with unset variable was sent")
	}
}

func TestEscapedPathAndQueryReachUpstream(t *testing.T) {
	f := newFixture(t, oneMirror(0), nil)
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, r.URL.EscapedPath()+"?"+r.URL.RawQuery)
	}
	_, body := get(t, f.srv.URL+"/pkg/acme%2Flib/repository/archive.zip?sha=abc")
	if body != "/acme%2Flib/repository/archive.zip?sha=abc" {
		t.Fatalf("upstream saw %q", body)
	}
}

func TestTooLargeIsNotCached(t *testing.T) {
	f := newFixture(t, oneMirror(0), nil)
	f.handler = func(w http.ResponseWriter, r *http.Request) { w.Write(make([]byte, 2048)) }
	if resp, _ := get(t, f.srv.URL+"/pkg/big.zip"); resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestConcurrentRequestsShareOneFetch(t *testing.T) {
	f := newFixture(t, oneMirror(0), nil)
	release := make(chan struct{})
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		<-release
		io.WriteString(w, "zip")
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, body := get(t, f.srv.URL+"/pkg/same.zip"); body != "zip" {
				t.Errorf("body %q", body)
			}
		}()
	}
	time.Sleep(100 * time.Millisecond) // let the requests pile up behind the first
	close(release)
	wg.Wait()
	if n := f.calls.Load(); n != 1 {
		t.Errorf("upstream calls = %d, want 1", n)
	}
}

func TestMirrorsDocument(t *testing.T) {
	f := newFixture(t, oneMirror(0), nil)
	_, body := get(t, f.srv.URL+"/mirrors.json")
	var doc MirrorsDocument
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Mirrors) != 1 || doc.Mirrors[0].Path != "/pkg/" || doc.Mirrors[0].Upstream != f.upstream.URL+"/" {
		t.Fatalf("doc = %+v", doc)
	}
}

func TestBadPaths(t *testing.T) {
	f := newFixture(t, oneMirror(0), nil)
	f.handler = func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "x") }
	if resp, _ := get(t, f.srv.URL+"/nope/x.zip"); resp.StatusCode != 404 {
		t.Errorf("unknown mirror: %d", resp.StatusCode)
	}
	if resp, _ := get(t, f.srv.URL+"/pkg/"); resp.StatusCode != 400 {
		t.Errorf("empty path: %d", resp.StatusCode)
	}
}
