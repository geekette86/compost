// Package cache is Compost's on-disk heap: cached upstream responses stored as
// a data file plus a small JSON metadata file, both written atomically.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// ErrTooLarge is returned by Put when the body exceeds the size limit.
var ErrTooLarge = errors.New("object exceeds max_object_size")

// Meta describes a cached object.
type Meta struct {
	Key          string    `json:"key"`
	URL          string    `json:"url"`
	StoredAt     time.Time `json:"stored_at"`
	ExpiresAt    time.Time `json:"expires_at,omitzero"` // zero: never expires
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"last_modified,omitempty"`
	ContentType  string    `json:"content_type,omitempty"`
	Size         int64     `json:"size"`
}

// Fresh reports whether the object may be served without revalidation.
func (m Meta) Fresh(now time.Time) bool {
	return m.ExpiresAt.IsZero() || now.Before(m.ExpiresAt)
}

// Entry is a cached object on disk.
type Entry struct {
	Meta
	Path string // data file
}

// Store keeps objects under a directory, sharded by the key's hash.
type Store struct {
	dir     string
	maxSize int64
}

// New creates the cache directory if needed. maxSize <= 0 disables the limit.
func New(dir string, maxSize int64) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "tmp"), 0o755); err != nil {
		return nil, fmt.Errorf("creating cache dir: %w", err)
	}
	return &Store{dir: dir, maxSize: maxSize}, nil
}

func (s *Store) paths(key string) (data, meta string) {
	sum := sha256.Sum256([]byte(key))
	h := hex.EncodeToString(sum[:])
	base := filepath.Join(s.dir, "objects", h[:2], h[2:4], h)
	return base + ".data", base + ".json"
}

// Get returns the entry for key, or an error satisfying
// errors.Is(err, os.ErrNotExist) when nothing is cached.
func (s *Store) Get(key string) (*Entry, error) {
	dataPath, metaPath := s.paths(key)
	b, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, err
	}
	var m Meta
	if err := json.Unmarshal(b, &m); err != nil || m.Key != key {
		// Corrupt metadata or a hash collision: treat as a miss.
		return nil, fmt.Errorf("bad cache metadata for %q: %w", key, os.ErrNotExist)
	}
	if _, err := os.Stat(dataPath); err != nil {
		return nil, err
	}
	return &Entry{Meta: m, Path: dataPath}, nil
}

// Put stores the body read from r under key. The data file is renamed into
// place before the metadata, so readers never see metadata without data.
func (s *Store) Put(key string, m Meta, r io.Reader) (*Entry, error) {
	dataPath, _ := s.paths(key)
	if err := os.MkdirAll(filepath.Dir(dataPath), 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Join(s.dir, "tmp"), "put-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name()) // no-op once renamed

	src := r
	if s.maxSize > 0 {
		src = io.LimitReader(r, s.maxSize+1)
	}
	n, err := io.Copy(tmp, src)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	if s.maxSize > 0 && n > s.maxSize {
		return nil, ErrTooLarge
	}
	if err := os.Rename(tmp.Name(), dataPath); err != nil {
		return nil, err
	}
	m.Key = key
	m.Size = n
	if err := s.writeMeta(key, m); err != nil {
		return nil, err
	}
	return &Entry{Meta: m, Path: dataPath}, nil
}

// Touch rewrites the metadata of an existing entry, e.g. after a 304.
func (s *Store) Touch(key string, m Meta) error {
	m.Key = key
	return s.writeMeta(key, m)
}

func (s *Store) writeMeta(key string, m Meta) error {
	_, metaPath := s.paths(key)
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Join(s.dir, "tmp"), "meta-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(b)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), metaPath)
}
