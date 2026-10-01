// Package proxy implements Compost's caching reverse proxy.
//
// Every configured mirror is served under /<name>/. A request for
// /<name>/<path> is answered from the heap when a fresh copy exists; otherwise
// it is fetched from <upstream><path> (revalidating with If-None-Match /
// If-Modified-Since when a stale copy exists), stored and served. Concurrent
// requests for the same object share one upstream fetch, and a stale copy is
// served if the upstream is down.
package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/geekette86/compost/internal/cache"
	"github.com/geekette86/compost/internal/config"
)

// Cache status values reported in the X-Compost-Cache response header.
const (
	statusHit         = "HIT"
	statusMiss        = "MISS"
	statusRevalidated = "REVALIDATED"
	statusStale       = "STALE"
)

// Options holds the dependencies of a Server.
type Options struct {
	Config  config.Config
	Store   *cache.Store
	Client  *http.Client        // defaults to a client with the configured timeout
	Logger  *slog.Logger        // defaults to slog.Default()
	Getenv  func(string) string // defaults to os.Getenv; used for header expansion
	Now     func() time.Time    // defaults to time.Now
	Version string
}

// Server is the HTTP handler for the proxy.
type Server struct {
	opts    Options
	mirrors []*mirror
	byName  map[string]*mirror
	flights flightGroup
	mux     *http.ServeMux
}

type mirror struct {
	config.Mirror
	allowed []*regexp.Regexp
	headers map[string]string

	hits, misses, revalidated, stale, errors atomic.Int64
}

// New builds a Server. The configuration must already be valid.
func New(opts Options) (*Server, error) {
	if opts.Store == nil {
		return nil, errors.New("proxy: Store is required")
	}
	if opts.Client == nil {
		opts.Client = &http.Client{Timeout: time.Duration(opts.Config.UpstreamTimeout)}
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Getenv == nil {
		opts.Getenv = os.Getenv
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}

	s := &Server{opts: opts, byName: map[string]*mirror{}}
	for _, mc := range opts.Config.Mirrors {
		m := &mirror{Mirror: mc, headers: config.ExpandHeaders(mc.Headers, opts.Getenv)}
		for _, p := range mc.AllowedPaths {
			re, err := regexp.Compile(p)
			if err != nil {
				return nil, fmt.Errorf("mirror %q: %w", mc.Name, err)
			}
			m.allowed = append(m.allowed, re)
		}
		s.mirrors = append(s.mirrors, m)
		s.byName[m.Name] = m
	}

	s.mux = http.NewServeMux()
	s.mux.HandleFunc("GET /{$}", s.handleIndex)
	s.mux.HandleFunc("GET /mirrors.json", s.handleMirrors)
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, "ok\n")
	})
	s.mux.HandleFunc("GET /stats", s.handleStats)
	s.mux.HandleFunc("GET /{mirror}/{path...}", s.handleObject)
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Server", "compost/"+s.opts.Version)
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "compost %s: your Composer downloads, recycled locally.\n\n", s.opts.Version)
	for _, m := range s.mirrors {
		fmt.Fprintf(w, "  /%s/  ->  %s\n", m.Name, m.Upstream)
	}
	io.WriteString(w, "\nDiscovery: /mirrors.json   Stats: /stats\n")
}

// MirrorsDocument is the discovery document served at /mirrors.json and read
// by the Compost Composer plugin.
type MirrorsDocument struct {
	Name    string           `json:"name"`
	Version string           `json:"version"`
	Mirrors []MirrorDocEntry `json:"mirrors"`
}

// MirrorDocEntry tells the plugin to rewrite URLs starting with Upstream to
// <proxy base URL><Path><rest of URL>.
type MirrorDocEntry struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Upstream string `json:"upstream"`
}

func (s *Server) handleMirrors(w http.ResponseWriter, _ *http.Request) {
	doc := MirrorsDocument{Name: "compost", Version: s.opts.Version, Mirrors: []MirrorDocEntry{}}
	for _, m := range s.mirrors {
		doc.Mirrors = append(doc.Mirrors, MirrorDocEntry{Name: m.Name, Path: "/" + m.Name + "/", Upstream: m.Upstream})
	}
	writeJSON(w, doc)
}

func (s *Server) handleStats(w http.ResponseWriter, _ *http.Request) {
	type mirrorStats struct {
		Hits        int64 `json:"hits"`
		Misses      int64 `json:"misses"`
		Revalidated int64 `json:"revalidated"`
		Stale       int64 `json:"stale"`
		Errors      int64 `json:"errors"`
	}
	out := map[string]mirrorStats{}
	for _, m := range s.mirrors {
		out[m.Name] = mirrorStats{m.hits.Load(), m.misses.Load(), m.revalidated.Load(), m.stale.Load(), m.errors.Load()}
	}
	writeJSON(w, map[string]any{"mirrors": out})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

// upstreamError carries an upstream status that should reach the client.
type upstreamError struct{ status int }

func (e *upstreamError) Error() string { return fmt.Sprintf("upstream returned %d", e.status) }

type fetchResult struct {
	entry  *cache.Entry
	status string
}

func (s *Server) handleObject(w http.ResponseWriter, r *http.Request) {
	m, ok := s.byName[r.PathValue("mirror")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	// Use the escaped path so encoded characters (GitLab's vendor%2Fproject)
	// reach the upstream unchanged.
	rel := strings.TrimPrefix(r.URL.EscapedPath(), "/"+m.Name+"/")
	if !validPath(rel) {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	if !m.permits(rel) {
		http.Error(w, "path not allowed for mirror "+m.Name, http.StatusForbidden)
		return
	}
	upstreamURL := m.Upstream + rel
	key := m.Name + "/" + rel
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
		key += "?" + r.URL.RawQuery
	}

	log := s.opts.Logger.With("mirror", m.Name, "path", rel)
	entry, err := s.opts.Store.Get(key)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Warn("cache read failed", "err", err)
	}
	if entry != nil && entry.Fresh(s.opts.Now()) {
		m.hits.Add(1)
		s.serve(w, r, entry, statusHit)
		return
	}

	// Detach from the client's context: the fetch is shared with other
	// waiters and is worth finishing even if this client goes away.
	ctx := context.WithoutCancel(r.Context())
	res, err := s.flights.do(key, func() (fetchResult, error) {
		return s.fetch(ctx, m, key, upstreamURL, entry)
	})
	if err != nil {
		m.errors.Add(1)
		var ue *upstreamError
		if errors.As(err, &ue) && ue.status >= 400 && ue.status < 500 {
			log.Info("upstream refused", "status", ue.status)
			http.Error(w, http.StatusText(ue.status), ue.status)
			return
		}
		log.Error("upstream fetch failed", "err", err)
		http.Error(w, "upstream fetch failed", http.StatusBadGateway)
		return
	}
	switch res.status {
	case statusMiss:
		m.misses.Add(1)
	case statusRevalidated:
		m.revalidated.Add(1)
	case statusStale:
		m.stale.Add(1)
	}
	s.serve(w, r, res.entry, res.status)
}

// validPath rejects empty paths and dot segments.
func validPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

func (m *mirror) permits(p string) bool {
	if len(m.allowed) == 0 {
		return true
	}
	for _, re := range m.allowed {
		if re.MatchString(p) {
			return true
		}
	}
	return false
}

func (m *mirror) expiry(now time.Time) time.Time {
	if m.TTL == 0 {
		return time.Time{}
	}
	return now.Add(time.Duration(m.TTL))
}

// fetch retrieves upstreamURL, storing the result under key. stale, if not
// nil, is the expired cached copy used for revalidation and as a fallback.
func (s *Server) fetch(ctx context.Context, m *mirror, key, upstreamURL string, stale *cache.Entry) (fetchResult, error) {
	log := s.opts.Logger.With("mirror", m.Name, "url", upstreamURL)
	fallback := func(err error) (fetchResult, error) {
		if stale != nil {
			log.Warn("serving stale copy", "err", err)
			return fetchResult{stale, statusStale}, nil
		}
		return fetchResult{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstreamURL, nil)
	if err != nil {
		return fetchResult{}, err
	}
	req.Header.Set("User-Agent", "compost/"+s.opts.Version+" (+https://github.com/geekette86/compost)")
	for k, v := range m.headers {
		req.Header.Set(k, v)
	}
	if stale != nil {
		if stale.ETag != "" {
			req.Header.Set("If-None-Match", stale.ETag)
		}
		if stale.LastModified != "" {
			req.Header.Set("If-Modified-Since", stale.LastModified)
		}
	}

	start := s.opts.Now()
	resp, err := s.opts.Client.Do(req)
	if err != nil {
		return fallback(err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotModified && stale != nil:
		meta := stale.Meta
		meta.ExpiresAt = m.expiry(s.opts.Now())
		if err := s.opts.Store.Touch(key, meta); err != nil {
			log.Warn("updating cache metadata failed", "err", err)
		}
		stale.Meta = meta
		log.Debug("revalidated", "took", s.opts.Now().Sub(start))
		return fetchResult{stale, statusRevalidated}, nil

	case resp.StatusCode == http.StatusOK:
		now := s.opts.Now()
		entry, err := s.opts.Store.Put(key, cache.Meta{
			URL:          upstreamURL,
			StoredAt:     now,
			ExpiresAt:    m.expiry(now),
			ETag:         resp.Header.Get("ETag"),
			LastModified: resp.Header.Get("Last-Modified"),
			ContentType:  resp.Header.Get("Content-Type"),
		}, resp.Body)
		if err != nil {
			return fallback(fmt.Errorf("storing response: %w", err))
		}
		log.Info("stored", "bytes", entry.Size, "took", s.opts.Now().Sub(start))
		return fetchResult{entry, statusMiss}, nil

	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		// The object is gone or never existed; do not mask that with a stale copy.
		return fetchResult{}, &upstreamError{resp.StatusCode}

	default:
		return fallback(&upstreamError{resp.StatusCode})
	}
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request, e *cache.Entry, status string) {
	f, err := os.Open(e.Path)
	if err != nil {
		s.opts.Logger.Error("opening cached object", "path", e.Path, "err", err)
		http.Error(w, "cache read failed", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	h := w.Header()
	h.Set("X-Compost-Cache", status)
	if e.ContentType != "" {
		h.Set("Content-Type", e.ContentType)
	}
	if e.ETag != "" {
		h.Set("ETag", e.ETag)
	}
	modTime := e.StoredAt
	if t, err := http.ParseTime(e.LastModified); err == nil {
		modTime = t
	}
	// ServeContent handles HEAD, Range and conditional requests for us.
	http.ServeContent(w, r, "", modTime, f)
}

// flightGroup collapses concurrent fetches of the same key into one.
type flightGroup struct {
	mu    sync.Mutex
	calls map[string]*flight
}

type flight struct {
	done chan struct{}
	res  fetchResult
	err  error
}

func (g *flightGroup) do(key string, fn func() (fetchResult, error)) (fetchResult, error) {
	g.mu.Lock()
	if g.calls == nil {
		g.calls = map[string]*flight{}
	}
	if f, ok := g.calls[key]; ok {
		g.mu.Unlock()
		<-f.done
		return f.res, f.err
	}
	f := &flight{done: make(chan struct{})}
	g.calls[key] = f
	g.mu.Unlock()

	defer func() {
		g.mu.Lock()
		delete(g.calls, key)
		g.mu.Unlock()
		close(f.done)
	}()
	f.res, f.err = fn()
	return f.res, f.err
}
