package cache

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPutGetTouch(t *testing.T) {
	s, err := New(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("k"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty cache: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	e, err := s.Put("k", Meta{URL: "https://x/k", StoredAt: now, ETag: `"1"`}, strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if e.Size != 5 {
		t.Fatalf("size %d", e.Size)
	}
	got, err := s.Get("k")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(got.Path)
	if string(b) != "hello" || got.ETag != `"1"` || !got.Fresh(now.Add(time.Hour)) {
		t.Fatalf("got %+v %q", got.Meta, b)
	}

	got.ExpiresAt = now.Add(time.Minute)
	if err := s.Touch("k", got.Meta); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get("k")
	if got.Fresh(now.Add(2 * time.Minute)) {
		t.Fatal("entry should be stale after its expiry")
	}
}

func TestPutTooLarge(t *testing.T) {
	s, _ := New(t.TempDir(), 4)
	if _, err := s.Put("k", Meta{}, strings.NewReader("hello")); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v", err)
	}
	if _, err := s.Get("k"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oversized object was stored: %v", err)
	}
}
