// Package history stores durable, bounded snapshots independently of preview
// cache. Viewing history never touches a searched host or filesystem path.
package history

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/daviddwlee84/lazyfind/internal/config"
	"github.com/daviddwlee84/lazyfind/internal/domain"
	_ "modernc.org/sqlite"
)

const schemaVersion = 1

// ErrNotFound distinguishes an unavailable snapshot from a storage failure.
var ErrNotFound = errors.New("history run not found")

type Store struct {
	cfg  config.Config
	path string
	mu   sync.Mutex
}

func New(cfg config.Config) *Store {
	return &Store{cfg: cfg, path: filepath.Join(cfg.Paths.State, "history.db")}
}

// Path exposes the actual database location for diagnostics.
func (s *Store) Path() string { return s.path }

func (s *Store) open(ctx context.Context, write bool) (*sql.DB, error) {
	if s.cfg.Paths.State == "" {
		return nil, errors.New("history state directory is empty")
	}
	_, statErr := os.Stat(s.path)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	if !write && errors.Is(statErr, os.ErrNotExist) {
		return nil, os.ErrNotExist
	}
	if write {
		if err := os.MkdirAll(s.cfg.Paths.State, 0700); err != nil {
			return nil, fmt.Errorf("create history directory: %w", err)
		}
		// Precreate with private permissions instead of allowing SQLite's default
		// file mode to disclose paths and saved excerpts to other local users.
		if errors.Is(statErr, os.ErrNotExist) {
			f, err := os.OpenFile(s.path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
			if err != nil && !errors.Is(err, os.ErrExist) {
				return nil, err
			}
			if f != nil {
				if err = f.Close(); err != nil {
					return nil, err
				}
			}
		}
	}
	u := url.URL{Scheme: "file", Path: s.path}
	query := u.Query()
	query.Set("mode", "ro")
	if write {
		query.Set("mode", "rw")
	}
	query.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	fail := func(err error) (*sql.DB, error) { _ = db.Close(); return nil, err }
	var version int
	if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fail(fmt.Errorf("read history schema: %w", err))
	}
	if version > schemaVersion {
		return fail(fmt.Errorf("history schema version %d is newer than supported version %d; update lazyfind before opening this database", version, schemaVersion))
	}
	if version == 0 {
		if !write {
			return fail(errors.New("history database has not been initialized"))
		}
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return fail(e)
		}
		if _, e = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS runs (
			id TEXT PRIMARY KEY,
			started_at INTEGER NOT NULL,
			captured_at INTEGER NOT NULL,
			finalized INTEGER NOT NULL DEFAULT 0,
			pinned INTEGER NOT NULL DEFAULT 0,
			summary BLOB NOT NULL,
			snapshot BLOB NOT NULL
		);
		CREATE INDEX IF NOT EXISTS runs_captured ON runs(captured_at DESC);
		PRAGMA user_version = 1;`); e != nil {
			_ = tx.Rollback()
			return fail(fmt.Errorf("initialize history: %w", e))
		}
		if e = tx.Commit(); e != nil {
			return fail(e)
		}
	}
	if write {
		// WAL allows another lazyfind process to read old snapshots while a new
		// confirmed run is being updated. Unknown schemas were rejected above.
		if _, err = db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
			return fail(err)
		}
		if err = os.Chmod(s.path, 0600); err != nil {
			return fail(err)
		}
	}
	return db, nil
}

// Save upserts one confirmed run. It preserves a pin applied between partial
// and final snapshots. A failure is returned without affecting the live search.
func (s *Store) Save(ctx context.Context, run domain.Run) error {
	if !s.cfg.History.Enabled {
		return nil
	}
	if run.ID == "" {
		return errors.New("cannot save history run without an ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run = s.bounded(run)
	if run.CapturedAt.IsZero() {
		run.CapturedAt = time.Now()
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = run.CapturedAt
	}
	snapshot, err := json.Marshal(run)
	if err != nil {
		return fmt.Errorf("encode history snapshot: %w", err)
	}
	summaryRun := run
	summaryRun.Items = nil
	summary, err := json.Marshal(summaryRun)
	if err != nil {
		return err
	}
	db, err := s.open(ctx, true)
	if err != nil {
		return err
	}
	defer db.Close()
	finalized := run.FinishedAt != nil || (run.Status != "" && run.Status != "running" && run.Status != "searching")
	_, err = db.ExecContext(ctx, `INSERT INTO runs(id,started_at,captured_at,finalized,pinned,summary,snapshot)
		VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET
		started_at=excluded.started_at,captured_at=excluded.captured_at,
		finalized=excluded.finalized,summary=excluded.summary,snapshot=excluded.snapshot
		WHERE excluded.captured_at>=runs.captured_at AND excluded.finalized>=runs.finalized`,
		run.ID, run.StartedAt.UnixNano(), run.CapturedAt.UnixNano(), boolInt(finalized), boolInt(run.Pinned), summary, snapshot)
	if err != nil {
		return fmt.Errorf("save history: %w", err)
	}
	return s.pruneDB(ctx, db)
}

func (s *Store) bounded(run domain.Run) domain.Run {
	limit := s.cfg.History.MaxResults
	if limit < 1 {
		limit = 5000
	}
	if len(run.Items) > limit {
		run.Items = run.Items[:limit]
		run.SnapshotTruncated = true
	}
	items := make([]domain.Item, len(run.Items))
	copy(items, run.Items)
	run.Items = items
	remaining := s.cfg.History.SnippetTotalBytes
	for i := range run.Items {
		item := &run.Items[i]
		matches := item.Matches
		maxMatches := s.cfg.Search.MaxMatches
		if maxMatches < 1 {
			maxMatches = 50
		}
		if len(matches) > maxMatches {
			matches = matches[:maxMatches]
			item.MatchesTruncated = true
			run.SnapshotTruncated = true
		}
		item.Matches = append([]domain.Match(nil), matches...)
		snippets := 0
		for j := range item.Matches {
			match := &item.Matches[j]
			if match.Text == "" {
				continue
			}
			before := match.Text
			if snippets >= s.cfg.History.SnippetsPerItem || remaining <= 0 || s.cfg.History.SnippetBytes <= 0 {
				match.Text = ""
			} else {
				budget := s.cfg.History.SnippetBytes
				if remaining < budget {
					budget = remaining
				}
				match.Text = clipUTF8(match.Text, budget)
				remaining -= len(match.Text)
				snippets++
			}
			if match.Text != before {
				run.SnapshotTruncated = true
			}
		}
	}
	return run
}

// List reads small summaries, most recently captured first. No database or
// directories are created when history does not yet exist.
func (s *Store) List(ctx context.Context, limit int) ([]domain.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.open(ctx, false)
	if errors.Is(err, os.ErrNotExist) {
		return []domain.Run{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if limit <= 0 {
		limit = -1 // SQLite: all retained summaries, including old pinned runs.
	}
	rows, err := db.QueryContext(ctx, "SELECT summary,pinned FROM runs ORDER BY captured_at DESC,id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []domain.Run{}
	for rows.Next() {
		var data []byte
		var pinned int
		if err = rows.Scan(&data, &pinned); err != nil {
			return nil, err
		}
		var run domain.Run
		if err = json.Unmarshal(data, &run); err != nil {
			return nil, fmt.Errorf("decode history summary: %w", err)
		}
		run.Pinned = pinned != 0
		run.Items = nil
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *Store) Get(ctx context.Context, id string) (domain.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.open(ctx, false)
	if errors.Is(err, os.ErrNotExist) {
		return domain.Run{}, ErrNotFound
	}
	if err != nil {
		return domain.Run{}, err
	}
	defer db.Close()
	var data []byte
	var pinned int
	err = db.QueryRowContext(ctx, "SELECT snapshot,pinned FROM runs WHERE id=?", id).Scan(&data, &pinned)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Run{}, ErrNotFound
	}
	if err != nil {
		return domain.Run{}, err
	}
	var run domain.Run
	if err = json.Unmarshal(data, &run); err != nil {
		return domain.Run{}, fmt.Errorf("decode history run %s: %w", id, err)
	}
	run.Pinned = pinned != 0
	return run, nil
}

func (s *Store) Pin(ctx context.Context, id string, pinned bool) error {
	return s.mutateExisting(ctx, "UPDATE runs SET pinned=? WHERE id=?", boolInt(pinned), id)
}
func (s *Store) Delete(ctx context.Context, id string) error {
	return s.mutateExisting(ctx, "DELETE FROM runs WHERE id=?", id)
}
func (s *Store) mutateExisting(ctx context.Context, query string, args ...any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(s.path); errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	db, err := s.open(ctx, true)
	if err != nil {
		return err
	}
	defer db.Close()
	result, err := db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Prune(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(s.path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	db, err := s.open(ctx, true)
	if err != nil {
		return err
	}
	defer db.Close()
	return s.pruneDB(ctx, db)
}
func (s *Store) pruneDB(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if s.cfg.History.MaxDays > 0 {
		cutoff := time.Now().AddDate(0, 0, -s.cfg.History.MaxDays).UnixNano()
		if _, err = tx.ExecContext(ctx, "DELETE FROM runs WHERE pinned=0 AND captured_at < ?", cutoff); err != nil {
			return fmt.Errorf("prune history age: %w", err)
		}
	}
	if s.cfg.History.MaxRuns > 0 {
		if _, err = tx.ExecContext(ctx, `DELETE FROM runs WHERE pinned=0 AND id IN
			(SELECT id FROM runs WHERE pinned=0 ORDER BY captured_at DESC,id DESC LIMIT -1 OFFSET ?)`, s.cfg.History.MaxRuns); err != nil {
			return fmt.Errorf("prune history count: %w", err)
		}
	}
	return tx.Commit()
}
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
func clipUTF8(value string, max int) string {
	value = strings.ToValidUTF8(value, "�")
	if len(value) <= max {
		return value
	}
	if max <= 0 {
		return ""
	}
	for max > 0 && !utf8.RuneStart(value[max]) {
		max--
	}
	return value[:max]
}
