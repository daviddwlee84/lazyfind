package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/daviddwlee84/lazyfind/internal/config"
	"github.com/daviddwlee84/lazyfind/internal/domain"
)

func testStore(t *testing.T) (*Store, config.Config) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Paths.State = filepath.Join(t.TempDir(), "state")
	cfg.History.MaxDays = 0
	cfg.History.MaxRuns = 0
	return New(cfg), cfg
}
func fixture(id string, at time.Time) domain.Run {
	return domain.Run{SchemaVersion: 1, ID: id, StartedAt: at, CapturedAt: at, Status: "complete", Query: domain.QuerySpec{Raw: "needle mtime:<7d", Text: "needle", Roots: []string{"/missing/offline"}, Target: domain.Target{Host: "offline.example"}}, Items: []domain.Item{{ID: "item", Path: "/missing/offline/file.txt", Matches: []domain.Match{{Source: "text", Line: 19, Text: "a saved matching excerpt"}}}}, Observed: 1}
}

func TestMissingHistoryReadsDoNotCreateState(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	rows, err := s.List(ctx, 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows=%v error=%v", rows, err)
	}
	if _, err = s.Get(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get: %v", err)
	}
	if err = s.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(s.cfg.Paths.State); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read created state: %v", err)
	}
}

func TestUnlimitedSummaryListIncludesOlderPinnedRun(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	now := time.Now()
	for n, id := range []string{"pinned-old", "new"} {
		if err := s.Save(ctx, fixture(id, now.Add(time.Duration(n)*time.Hour))); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Pin(ctx, "pinned-old", true); err != nil {
		t.Fatal(err)
	}
	limited, err := s.List(ctx, 1)
	if err != nil || len(limited) != 1 || limited[0].ID != "new" {
		t.Fatalf("limited: %v %v", limited, err)
	}
	all, err := s.List(ctx, 0)
	if err != nil || len(all) != 2 || !all[1].Pinned {
		t.Fatalf("all: %v %v", all, err)
	}
}
func TestSnapshotRoundTripPinAndFinalization(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	run := fixture("one", time.Now())
	run.Status = "running"
	if err := s.Save(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := s.Pin(ctx, run.ID, true); err != nil {
		t.Fatal(err)
	}
	finish := run.CapturedAt.Add(time.Second)
	run.CapturedAt = finish
	run.FinishedAt = &finish
	run.Status = "complete"
	if err := s.Save(ctx, run); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Pinned || got.Status != "complete" || got.Items[0].Matches[0].Line != 19 {
		t.Fatalf("bad persisted result: %+v", got)
	}
	rows, err := s.List(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Items != nil || !rows[0].Pinned || rows[0].Observed != 1 {
		t.Fatalf("summaries: %+v", rows)
	}
	if err = s.Pin(ctx, run.ID, false); err != nil {
		t.Fatal(err)
	}
	if err = s.Delete(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Get(ctx, run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted result: %v", err)
	}
	info, err := os.Stat(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("history mode = %o", info.Mode().Perm())
	}
}
func TestBoundedSnapshotsDoNotMutateLiveResults(t *testing.T) {
	_, cfg := testStore(t)
	cfg.History.MaxResults = 2
	cfg.Search.MaxMatches = 3
	cfg.History.SnippetsPerItem = 2
	cfg.History.SnippetBytes = 5
	cfg.History.SnippetTotalBytes = 9
	s := New(cfg)
	ctx := context.Background()
	run := fixture("bounded", time.Now())
	run.Items = nil
	for i := 0; i < 3; i++ {
		run.Items = append(run.Items, domain.Item{ID: fmt.Sprint(i), MatchCount: 4, Matches: []domain.Match{{Line: 1, Text: "中文 hello"}, {Line: 2, Text: "abcdef"}, {Line: 3, Text: "third"}, {Line: 4, Text: "fourth"}}})
	}
	if err := s.Save(ctx, run); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 2 || !got.SnapshotTruncated || !got.Items[0].MatchesTruncated {
		t.Fatalf("limits not marked: %+v", got)
	}
	total := 0
	for _, item := range got.Items {
		snippets := 0
		for _, match := range item.Matches {
			if match.Text != "" {
				snippets++
			}
			total += len(match.Text)
			if len(match.Text) > 5 || !utf8.ValidString(match.Text) {
				t.Fatalf("invalid snippet: %q", match.Text)
			}
		}
		if snippets > 2 {
			t.Fatal("too many snippets")
		}
	}
	if total > 9 {
		t.Fatalf("snippet budget exceeded: %d", total)
	}
	if len(run.Items) != 3 || run.Items[0].Matches[0].Text != "中文 hello" || len(run.Items[0].Matches) != 4 {
		t.Fatal("save mutated the live search")
	}
}
func TestRetentionKeepsPinnedSnapshots(t *testing.T) {
	_, cfg := testStore(t)
	cfg.History.MaxDays = 2
	cfg.History.MaxRuns = 1
	s := New(cfg)
	ctx := context.Background()
	now := time.Now()
	old := fixture("pinned", now.AddDate(0, 0, -20))
	old.Pinned = true
	for _, run := range []domain.Run{old, fixture("expired", now.AddDate(0, 0, -10)), fixture("older", now.Add(-time.Minute)), fixture("newest", now)} {
		if err := s.Save(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.List(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != "newest" || rows[1].ID != "pinned" {
		t.Fatalf("retained rows: %+v", rows)
	}
}
func TestConcurrentAndLateSnapshotsCannotRegressFinal(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	at := time.Now()
	final := fixture("race", at.Add(time.Minute))
	final.FinishedAt = &final.CapturedAt
	if err := s.Save(ctx, final); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			stale := fixture("race", at.Add(time.Duration(i)*time.Second))
			stale.Status = "running"
			if i == 7 {
				stale.CapturedAt = at.Add(time.Hour)
			}
			failures <- s.Save(ctx, stale)
		}(i)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Get(ctx, "race")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "complete" || !got.CapturedAt.Equal(final.CapturedAt) {
		t.Fatalf("final snapshot regressed: %+v", got)
	}
}

func TestIndependentStoreInstancesCanSaveConcurrently(t *testing.T) {
	_, cfg := testStore(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	failures := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			failures <- New(cfg).Save(ctx, fixture(fmt.Sprint(i), time.Now()))
		}(i)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	runs, err := New(cfg).List(ctx, 10)
	if err != nil || len(runs) != 6 {
		t.Fatalf("concurrent saves lost runs: %d %v", len(runs), err)
	}
}
func TestNewerDatabaseVersionRefusedWithoutMutation(t *testing.T) {
	s, _ := testStore(t)
	if err := os.MkdirAll(s.cfg.Paths.State, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("PRAGMA user_version=99; CREATE TABLE sentinel(value TEXT); INSERT INTO sentinel VALUES('preserved')"); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	err = s.Save(context.Background(), fixture("new", time.Now()))
	if err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("unexpected error: %v", err)
	}
	db, err = sql.Open("sqlite", s.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var value string
	if err = db.QueryRow("SELECT value FROM sentinel").Scan(&value); err != nil || value != "preserved" {
		t.Fatalf("database changed: %s %v", value, err)
	}
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 99 {
		t.Fatalf("schema changed: %d %v", version, err)
	}
}
func TestDisabledHistoryDoesNotWrite(t *testing.T) {
	_, cfg := testStore(t)
	cfg.History.Enabled = false
	s := New(cfg)
	if err := s.Save(context.Background(), fixture("disabled", time.Now())); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("disabled history wrote a database")
	}
}
