package history

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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

func TestSnapshotClippingKeepsOnlyVisibleSpansWithoutMutatingLive(t *testing.T) {
	_, cfg := testStore(t)
	cfg.History.SnippetBytes = 8
	cfg.History.SnippetsPerItem = 2
	cfg.History.SnippetTotalBytes = 11
	s := New(cfg)
	run := fixture("highlight", time.Now())
	raw := &domain.Span{Start: 8, End: 18}
	run.Items[0].Matches = []domain.Match{
		{Text: "ab中文defgh", Spans: []domain.Span{{Start: 2, End: 10}}, RawSpan: raw},
		{Text: "xyzmore", Spans: []domain.Span{{Start: 0, End: 7}}, RawSpan: raw},
		{Text: "not saved", Spans: []domain.Span{{Start: 0, End: 3}}, RawSpan: raw},
	}
	before, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	matches := got.Items[0].Matches
	if matches[0].Text != "ab中…" || !reflect.DeepEqual(matches[0].Spans, []domain.Span{{Start: 2, End: 5}}) {
		t.Fatalf("bad clipped first match: %+v", matches[0])
	}
	if matches[1].Text != "…" || len(matches[1].Spans) != 0 || matches[2].Text != "" || len(matches[2].Spans) != 0 {
		t.Fatalf("out-of-budget spans retained: %+v", matches)
	}
	for _, m := range matches {
		if !reflect.DeepEqual(m.RawSpan, raw) {
			t.Fatalf("raw locator changed: %+v", m)
		}
	}
	after, _ := json.Marshal(run)
	if string(before) != string(after) {
		t.Fatal("clipping mutated live span slices")
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

// writeV1 creates the preceding release's durable schema without exercising the
// current writer, so read-only compatibility and the real migration are tested.
func writeV1(t *testing.T, s *Store, run domain.Run) {
	t.Helper()
	if err := os.MkdirAll(s.cfg.Paths.State, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", s.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE runs (
		id TEXT PRIMARY KEY, started_at INTEGER NOT NULL, captured_at INTEGER NOT NULL,
		finalized INTEGER NOT NULL DEFAULT 0, pinned INTEGER NOT NULL DEFAULT 0,
		summary BLOB NOT NULL, snapshot BLOB NOT NULL
	); CREATE INDEX runs_captured ON runs(captured_at DESC); PRAGMA user_version=1;`)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	summaryRun := run
	summaryRun.Items = nil
	summary, err := json.Marshal(summaryRun)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO runs VALUES(?,?,?,?,?,?,?)", run.ID, run.StartedAt.UnixNano(), run.CapturedAt.UnixNano(), 1, boolInt(run.Pinned), summary, snapshot); err != nil {
		t.Fatal(err)
	}
}

func databaseVersion(t *testing.T, s *Store) int {
	t.Helper()
	db, err := sql.Open("sqlite", s.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func TestV1HistoryReadIsOfflineAndDoesNotMigrate(t *testing.T) {
	s, _ := testStore(t)
	run := fixture("legacy", time.Now())
	run.Pinned = true
	writeV1(t, s, run)
	ctx := context.Background()
	got, err := s.Get(ctx, run.ID)
	if err != nil || !got.Pinned || got.Items[0].Matches[0].Line != 19 {
		t.Fatalf("legacy snapshot: %+v %v", got, err)
	}
	rows, err := s.List(ctx, 0)
	if err != nil || len(rows) != 1 || !rows[0].Pinned {
		t.Fatalf("legacy summaries: %+v %v", rows, err)
	}
	if version := databaseVersion(t, s); version != 1 {
		t.Fatalf("read migrated schema to %d", version)
	}
	if err := s.Save(ctx, fixture("new", time.Now().Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	if version := databaseVersion(t, s); version != schemaVersion {
		t.Fatalf("write schema = %d", version)
	}
	got, err = s.Get(ctx, run.ID)
	if err != nil || !got.Pinned || len(got.Items) != 1 || got.Items[0].Matches[0].Text != run.Items[0].Matches[0].Text {
		t.Fatalf("migration damaged snapshot: %+v %v", got, err)
	}
	rows, err = s.List(ctx, 0)
	if err != nil || len(rows) != 2 {
		t.Fatalf("migration lost history: %+v %v", rows, err)
	}
}

func TestConcurrentV1MigrationPreservesHistory(t *testing.T) {
	s, cfg := testStore(t)
	writeV1(t, s, fixture("legacy", time.Now()))
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for i := 0; i < cap(failures); i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			failures <- New(cfg).Save(context.Background(), fixture(fmt.Sprint(i), time.Now()))
		}(i)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.List(context.Background(), 0)
	if err != nil || len(rows) != 9 {
		t.Fatalf("concurrent migration lost records: %d %v", len(rows), err)
	}
}

func TestDeletionTombstoneRejectsConcurrentAndLateSaves(t *testing.T) {
	s, cfg := testStore(t)
	ctx := context.Background()
	run := fixture("deleted", time.Now())
	if err := s.Save(ctx, run); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 17)
	start := make(chan struct{})
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			late := run
			late.Status = "running"
			late.CapturedAt = run.CapturedAt.Add(time.Duration(i+1) * time.Hour)
			failures <- New(cfg).Save(ctx, late)
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		failures <- New(cfg).Delete(ctx, run.ID)
	}()
	close(start)
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	// A final snapshot saved during quit cannot resurrect the deleted run.
	run.CapturedAt = run.CapturedAt.Add(24 * time.Hour)
	if err := New(cfg).Save(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted record resurrected: %v", err)
	}
	if err := s.Delete(ctx, run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if err := s.Delete(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("nonexistent delete: %v", err)
	}
	// Repeating a query is a new run; tombstones target IDs, not query text.
	run.ParentID = run.ID
	run.ID = "rerun"
	if err := s.Save(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, run.ID); err != nil {
		t.Fatalf("rerun was suppressed: %v", err)
	}
}

func TestDeletionTombstoneIsDurableAcrossProcesses(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	if err := s.Save(ctx, fixture("cross-process", time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "cross-process"); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestLateHistorySaveSubprocess$")
	cmd.Env = append(os.Environ(), "LAZYFIND_HISTORY_TEST_STATE="+s.cfg.Paths.State)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("late writer: %s %v", out, err)
	}
	if _, err := s.Get(ctx, "cross-process"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("subprocess resurrected history: %v", err)
	}
}

func TestLateHistorySaveSubprocess(t *testing.T) {
	state := os.Getenv("LAZYFIND_HISTORY_TEST_STATE")
	if state == "" {
		return
	}
	cfg := config.Defaults()
	cfg.Paths.State = state
	if err := New(cfg).Save(context.Background(), fixture("cross-process", time.Now())); err != nil {
		t.Fatal(err)
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
	if _, err = s.List(context.Background(), 0); err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("list accepted newer schema: %v", err)
	}
	if _, err = s.Get(context.Background(), "new"); err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("get accepted newer schema: %v", err)
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
