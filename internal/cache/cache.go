// Package cache keeps disposable, size-bounded preview text. LRU bookkeeping
// uses filesystem modification times so no metadata database is required.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/daviddwlee84/lazyfind/internal/config"
)

type Store struct {
	dir      string
	maxBytes int64
	mu       sync.Mutex
}
type Stats struct {
	Path     string `json:"path"`
	Entries  int    `json:"entries"`
	Bytes    int64  `json:"bytes"`
	MaxBytes int64  `json:"max_bytes"`
}

func New(cfg config.Config) *Store {
	return &Store{dir: filepath.Join(cfg.Paths.Cache, "previews"), maxBytes: cfg.Cache.MaxBytes}
}
func (s *Store) filename(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:])+".preview")
}

func (s *Store) Get(key string) (string, bool) {
	if s.maxBytes <= 0 {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	name := s.filename(key)
	info, err := os.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() > s.maxBytes {
		return "", false
	}
	b, err := os.ReadFile(name)
	if err != nil {
		return "", false
	}
	now := time.Now()
	_ = os.Chtimes(name, now, now)
	return string(b), true
}

func (s *Store) Put(key, text string) error {
	if s.maxBytes <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if int64(len(text)) > s.maxBytes { // An oversized replacement must not leave a stale value.
		if err := os.Remove(s.filename(key)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if s.dir == "previews" || s.dir == "" {
		return errors.New("preview cache directory is empty")
	}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return fmt.Errorf("create preview cache: %w", err)
	}
	f, err := os.CreateTemp(s.dir, ".preview-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	_, err = f.WriteString(text)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(tmp, s.filename(key)); err != nil {
		return err
	}
	return s.prune()
}

type entry struct {
	name string
	size int64
	used time.Time
}

func (s *Store) entries() ([]entry, error) {
	files, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return []entry{}, nil
	}
	if err != nil {
		return nil, err
	}
	entries := []entry{}
	for _, file := range files {
		if !isCacheFile(file.Name()) || file.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := file.Info()
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		entries = append(entries, entry{name: filepath.Join(s.dir, file.Name()), size: info.Size(), used: info.ModTime()})
	}
	return entries, nil
}
func isCacheFile(name string) bool {
	if !strings.HasSuffix(name, ".preview") || len(name) != 64+len(".preview") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimSuffix(name, ".preview"))
	return err == nil
}
func (s *Store) prune() error {
	entries, err := s.entries()
	if err != nil {
		return err
	}
	var size int64
	for _, e := range entries {
		size += e.size
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].used.Equal(entries[j].used) {
			return entries[i].name < entries[j].name
		}
		return entries[i].used.Before(entries[j].used)
	})
	for _, e := range entries {
		if size <= s.maxBytes {
			break
		}
		if err = os.Remove(e.name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		size -= e.size
	}
	return nil
}
func (s *Store) Stats() (Stats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stats := Stats{Path: s.dir, MaxBytes: s.maxBytes}
	entries, err := s.entries()
	if err != nil {
		return stats, err
	}
	stats.Entries = len(entries)
	for _, entry := range entries {
		stats.Bytes += entry.size
	}
	return stats, nil
}

// Clear removes only recognized preview files. It cannot erase history or
// unrelated files a user may have placed in the cache directory.
func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	files, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, file := range files {
		if !isCacheFile(file.Name()) {
			continue
		}
		if err = os.Remove(filepath.Join(s.dir, file.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
