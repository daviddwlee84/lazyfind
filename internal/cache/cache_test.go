package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/daviddwlee84/lazyfind/internal/config"
)

func testCache(t *testing.T, max int64) *Store {
	t.Helper()
	cfg := config.Defaults()
	cfg.Paths.Cache = filepath.Join(t.TempDir(), "cache")
	cfg.Cache.MaxBytes = max
	return New(cfg)
}
func TestMissingReadsAndClearDoNotCreateDirectory(t *testing.T) {
	s := testCache(t, 50)
	if _, ok := s.Get("absent"); ok {
		t.Fatal("unexpected cache hit")
	}
	stats, err := s.Stats()
	if err != nil || stats.Bytes != 0 || stats.Entries != 0 {
		t.Fatalf("stats: %+v %v", stats, err)
	}
	if err = s.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(s.dir); !os.IsNotExist(err) {
		t.Fatalf("read created cache: %v", err)
	}
}
func TestLRUUsesRecentReadsAndEnforcesSize(t *testing.T) {
	s := testCache(t, 8)
	if err := s.Put("a", "aaaa"); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("b", "bbbb"); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	for _, key := range []string{"a", "b"} {
		if err := os.Chtimes(s.filename(key), old, old); err != nil {
			t.Fatal(err)
		}
	}
	if text, ok := s.Get("a"); !ok || text != "aaaa" {
		t.Fatal("cache round trip failed")
	}
	if err := s.Put("c", "cccc"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("b"); ok {
		t.Fatal("least recently read item was retained")
	}
	if _, ok := s.Get("a"); !ok {
		t.Fatal("recently read item was evicted")
	}
	stats, err := s.Stats()
	if err != nil || stats.Bytes != 8 || stats.Entries != 2 {
		t.Fatalf("stats: %+v %v", stats, err)
	}
}
func TestOversizedReplacementAndClearIsolation(t *testing.T) {
	s := testCache(t, 4)
	if err := s.Put("path/../unsafe", "1234"); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("path/../unsafe", "oversized"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("path/../unsafe"); ok {
		t.Fatal("oversized replacement retained stale text")
	}
	if err := s.Put("small", "ok"); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(s.dir, "history.db")
	if err := os.WriteFile(unrelated, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(unrelated); err != nil || string(got) != "keep" {
		t.Fatalf("unrelated data changed: %q %v", got, err)
	}
	stats, err := s.Stats()
	if err != nil || stats.Entries != 0 {
		t.Fatalf("cache was not cleared: %+v %v", stats, err)
	}
}
func TestDisabledAndSymlinkCache(t *testing.T) {
	s := testCache(t, 0)
	if err := s.Put("a", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.dir); !os.IsNotExist(err) {
		t.Fatal("disabled cache created directory")
	}
	s = testCache(t, 100)
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(target, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, s.filename("a")); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("a"); ok {
		t.Fatal("cache followed symlink")
	}
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal("clear removed symlink target")
	}
}
