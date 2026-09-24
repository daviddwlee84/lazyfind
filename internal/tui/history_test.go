package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazyfind/internal/config"
	"github.com/daviddwlee84/lazyfind/internal/domain"
	"github.com/daviddwlee84/lazyfind/internal/history"
)

func historyModel(t *testing.T) *Model {
	t.Helper()
	cfg := config.Defaults()
	dir := t.TempDir()
	cfg.Paths.State = filepath.Join(dir, "state")
	cfg.Paths.Cache = filepath.Join(dir, "cache")
	cfg.History.MaxDays = 0
	cfg.History.MaxRuns = 0
	// Search workers created by state-transition tests are immediately canceled;
	// no actual host/tool is needed to exercise snapshot ownership.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := New(ctx, cfg, domain.QuerySpec{Raw: "needle", Text: "needle", Roots: []string{dir}, Sources: []string{"names"}}, false)
	m.run = domain.Run{SchemaVersion: 1, ID: "live", Query: m.query, StartedAt: time.Now(), CapturedAt: time.Now(), Status: "running"}
	item := domain.Item{ID: "one", Name: "one.txt", Path: filepath.Join(dir, "one.txt"), Kind: "file", Matches: []domain.Match{{Source: "text", Line: 3, Text: "needle"}}}
	m.items = map[string]domain.Item{item.ID: item}
	m.rebuild()
	m.gen = 1
	m.searching = true
	t.Cleanup(func() {
		if m.cancel != nil {
			m.cancel()
		}
		if m.previewCancel != nil {
			m.previewCancel()
		}
	})
	return m
}

func runHistoryCommands(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	message := cmd()
	if batch, ok := message.(tea.BatchMsg); ok {
		var messages []tea.Msg
		for _, child := range batch {
			messages = append(messages, runHistoryCommands(t, child)...)
		}
		return messages
	}
	if saved, ok := message.(savedMsg); ok && saved.err != nil {
		t.Fatal(saved.err)
	}
	return []tea.Msg{message}
}

func TestRetiringConfirmedSearchStoresCanceledResults(t *testing.T) {
	m := historyModel(t)
	m.accepted = true
	runHistoryCommands(t, m.retire())
	run, err := history.New(m.cfg).Get(context.Background(), "live")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "canceled" || run.FinishedAt == nil || len(run.Items) != 1 || run.Observed != 1 {
		t.Fatalf("incomplete retired snapshot: %+v", run)
	}
	if m.accepted || m.searching {
		t.Fatal("retired run remains active")
	}
}

func TestSubmittingDraftDoesNotSaveUnacceptedPreviousGeneration(t *testing.T) {
	m := historyModel(t)
	m.dirty = true
	m.input.SetValue("new needle")
	runHistoryCommands(t, m.submit())
	if !m.accepted || m.run.ID != "" || m.query.Text != "new needle" {
		t.Fatalf("new generation not accepted: %+v", m.run)
	}
	if _, err := os.Stat(m.cfg.Paths.State); !os.IsNotExist(err) {
		t.Fatalf("unaccepted generation wrote history: %v", err)
	}
}

func TestScopeEventPreservesResolvedBoundsInPartialHistory(t *testing.T) {
	m := historyModel(t)
	m.accepted = true
	after := time.Now().Add(-7 * 24 * time.Hour)
	header := m.run
	header.Query.Target = domain.Target{Host: "offline"}
	header.Query.Roots = []string{"/remote/resolved"}
	header.ResolvedAfter = &after
	_, cmd := m.Update(eventsMsg{gen: m.gen, events: []domain.Event{{Kind: "scope", Run: &header}}})
	runHistoryCommands(t, cmd)
	run, err := history.New(m.cfg).Get(context.Background(), "live")
	if err != nil {
		t.Fatal(err)
	}
	if run.Query.Roots[0] != "/remote/resolved" || run.ResolvedAfter == nil || !run.ResolvedAfter.Equal(after) || len(run.Items) != 1 {
		t.Fatalf("scope was lost: %+v", run)
	}
}

func TestOpeningHistoryRetiresLiveRunAndNeverResavesArchive(t *testing.T) {
	m := historyModel(t)
	m.accepted = true
	m.dirty = true
	m.historyGen = 4
	m.overlay = &overlay{kind: "history"}
	archived := m.snapshot()
	archived.ID = "archive"
	archived.Status = "complete"
	archived.CapturedAt = time.Now().Add(-time.Hour)
	archived.Query.Target = domain.Target{Host: "never-contact-this-host"}
	if err := history.New(m.cfg).Save(context.Background(), archived); err != nil {
		t.Fatal(err)
	}
	oldEdit := m.edit
	_, cmd := m.Update(snapshotMsg{gen: 4, run: archived})
	runHistoryCommands(t, cmd)
	if !m.historical || m.dirty || m.accepted || m.edit <= oldEdit {
		t.Fatal("history view retained live/draft state")
	}
	if cmd := m.accept(); cmd != nil || m.accepted {
		t.Fatal("historical action tried to confirm/resave the snapshot")
	}
	run, err := history.New(m.cfg).Get(context.Background(), "archive")
	if err != nil {
		t.Fatal(err)
	}
	if !run.CapturedAt.Equal(archived.CapturedAt) {
		t.Fatal("historical capture time changed")
	}
	live, err := history.New(m.cfg).Get(context.Background(), "live")
	if err != nil || live.Status != "canceled" {
		t.Fatalf("active run lost when opening history: %+v %v", live, err)
	}
}

func TestLateStorageAndHistoryMessagesDoNotReplaceCurrentState(t *testing.T) {
	m := historyModel(t)
	m.status = "current search"
	_, _ = m.Update(savedMsg{gen: m.gen - 1, id: "live", err: errors.New("old error")})
	if m.status != "current search" {
		t.Fatal("stale save error changed new query")
	}
	_, _ = m.Update(savedMsg{gen: m.gen, id: "another", err: errors.New("old run")})
	if m.status != "current search" {
		t.Fatal("different run save error changed current state")
	}
	_, _ = m.Update(savedMsg{gen: m.gen, id: "live", err: errors.New("disk full")})
	if !strings.Contains(m.status, "disk full") {
		t.Fatal("current save error was hidden")
	}
	m.historyGen = 8
	m.overlay = &overlay{kind: "history"}
	_, _ = m.Update(snapshotMsg{gen: 7, run: domain.Run{ID: "stale"}})
	if m.run.ID != "live" {
		t.Fatal("stale history request replaced current run")
	}
}

func TestRerunKeepsParentLinkThroughEveryEngineHeader(t *testing.T) {
	m := historyModel(t)
	m.historical = true
	m.searching = false
	m.run.Status = "complete"
	m.nextParentID = "archive"
	_ = m.start(true)
	header := domain.Run{SchemaVersion: 1, ID: "fresh", Query: m.query, Status: "running", StartedAt: time.Now(), CapturedAt: time.Now()}
	_, cmd := m.Update(eventsMsg{gen: m.gen, events: []domain.Event{{Kind: "start", Run: &header}, {Kind: "scope", Run: &header}}})
	runHistoryCommands(t, cmd)
	if m.run.ParentID != "archive" {
		t.Fatal("start/scope lost parent link")
	}
	finished := time.Now()
	header.Status = "empty"
	header.FinishedAt = &finished
	header.CapturedAt = finished
	_, cmd = m.Update(eventsMsg{gen: m.gen, events: []domain.Event{{Kind: "done", Run: &header}}})
	runHistoryCommands(t, cmd)
	run, err := history.New(m.cfg).Get(context.Background(), "fresh")
	if err != nil {
		t.Fatal(err)
	}
	if run.ParentID != "archive" || run.Status != "empty" {
		t.Fatalf("rerun lineage not saved: %+v", run)
	}
}

func TestRemoteScopeChangeSavesOldScopeAndWaitsForSubmission(t *testing.T) {
	m := historyModel(t)
	m.accepted = true
	oldRoot := m.run.Query.Roots[0]
	m.query.Target = domain.Target{Host: "new-remote"}
	m.query.Roots = []string{"~/new"}
	runHistoryCommands(t, m.changedScope())
	if !m.dirty || m.searching || m.accepted {
		t.Fatal("remote draft was submitted implicitly")
	}
	run, err := history.New(m.cfg).Get(context.Background(), "live")
	if err != nil {
		t.Fatal(err)
	}
	if run.Query.Target.Remote() || run.Query.Roots[0] != oldRoot || run.Status != "canceled" {
		t.Fatalf("previous snapshot overwritten with draft scope: %+v", run)
	}
}

func TestHistoricalRefreshStartsNewRunWithoutEditingArchive(t *testing.T) {
	m := historyModel(t)
	m.historical = true
	m.searching = false
	m.accepted = false
	m.run.Status = "complete"
	archiveID := m.run.ID
	runHistoryCommands(t, m.command("refresh"))
	if m.historical || m.parentID != archiveID || !m.accepted {
		t.Fatal("history refresh did not create a linked new run")
	}
	if _, err := os.Stat(m.cfg.Paths.State); !os.IsNotExist(err) {
		t.Fatal("refresh wrote the old archived snapshot")
	}
}
