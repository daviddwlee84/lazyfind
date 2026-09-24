package tui

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazyfind/internal/actions"
	"github.com/daviddwlee84/lazyfind/internal/config"
	"github.com/daviddwlee84/lazyfind/internal/domain"
	"github.com/daviddwlee84/lazyfind/internal/preview"
)

func matchModel(t *testing.T) *Model {
	m := historyModel(t)
	m.focus = 1
	m.input.Blur()
	m.historical = true
	m.searching = false
	m.items = map[string]domain.Item{
		"one": {ID: "one", Name: "one.txt", Path: "/one.txt", Kind: "file", Sources: []string{"names", "text", "documents"}, Matches: []domain.Match{{Source: "text", Line: 3, Column: 2, Text: "first needle"}, {Source: "text", Line: 27, Column: 9, Text: "second needle"}, {Source: "documents", Line: 91, Text: "extracted needle", Extracted: true}}},
		"two": {ID: "two", Name: "two.txt", Path: "/two.txt", Kind: "file", Sources: []string{"names"}},
	}
	m.selection = "one"
	m.rebuild()
	m.resize()
	return m
}
func TestMatchNavigationAndSnapshotOrdering(t *testing.T) {
	m := matchModel(t)
	original := append([]domain.Match(nil), m.items["one"].Matches...)
	_ = m.command("next_match")
	if m.matchIndex != 1 || !strings.Contains(m.matchHeader(), "Match 2/3 · text line 27:9") {
		t.Fatalf("wrong match %s", m.matchHeader())
	}
	if !strings.Contains(m.viewport.View(), "second needle") {
		t.Fatal("selected snippet not visible")
	}
	item := m.actionItem()
	expanded, err := actions.Expand([]string{"+{line}"}, *item, "", "")
	if err != nil || !reflect.DeepEqual(expanded, []string{"+27"}) {
		t.Fatalf("editor locator %v %v", expanded, err)
	}
	if !reflect.DeepEqual(m.items["one"].Matches, original) || !reflect.DeepEqual(m.snapshot().Items[0].Matches, original) {
		t.Fatal("navigation changed snapshot order")
	}
	_ = m.command("next_match")
	if !strings.Contains(m.matchHeader(), "documents extracted line 91") {
		t.Fatalf("extracted label %s", m.matchHeader())
	}
	expanded, err = actions.Expand([]string{"+{line}"}, *m.actionItem(), "", "")
	if err != nil || expanded[0] != "+1" {
		t.Fatalf("extracted match treated as file line: %v %v", expanded, err)
	}
	_ = m.command("next_match")
	if m.matchIndex != 0 {
		t.Fatal("next match did not wrap")
	}
	_ = m.command("previous_match")
	if m.matchIndex != 2 {
		t.Fatal("previous match did not wrap")
	}
	_ = m.selectRow(1)
	if m.matchIndex != 0 || m.matchItemID != "two" || !strings.Contains(m.matchHeader(), "Sources: names") {
		t.Fatal("new item retained old hit")
	}
	_ = m.selectRow(0)
	if m.matchIndex != 0 {
		t.Fatal("selection should reset hit index")
	}
}
func liveMatchModel(t *testing.T) (*Model, string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(p, []byte("first text"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.History.Enabled = false
	cfg.Paths.Cache = filepath.Join(dir, "cache")
	cfg.Paths.State = filepath.Join(dir, "state")
	cfg.Actions = []config.Action{{ID: "fixture-preview", Label: "Fixture preview", Argv: []string{"cat", "{path}"}, Mode: "preview", Cwd: "{dir}"}}
	cfg.Rules = []config.Rule{{Kinds: []string{"file"}, Actions: []string{"fixture-preview"}}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m := New(ctx, cfg, domain.QuerySpec{Text: "text", Sources: []string{"text"}}, false)
	item := domain.Item{ID: "file", Name: "file.txt", Path: p, Kind: "file", Sources: []string{"text"}, Matches: []domain.Match{{Source: "text", Line: 3, Text: "first"}, {Source: "text", Line: 27, Text: "second"}}}
	m.items = map[string]domain.Item{item.ID: item}
	m.rebuild()
	m.focus = 1
	m.input.Blur()
	m.resize()
	t.Cleanup(func() {
		if m.previewCancel != nil {
			m.previewCancel()
		}
	})
	return m, p
}
func previewEffect(t *testing.T, cmd tea.Cmd) previewMsg {
	t.Helper()
	for _, message := range runHistoryCommands(t, cmd) {
		if preview, ok := message.(previewMsg); ok {
			return preview
		}
	}
	t.Fatal("effect did not produce preview message")
	return previewMsg{}
}
func TestSelectedMatchReachesBuiltinEditor(t *testing.T) {
	t.Setenv("VISUAL", "vi")
	m, _ := liveMatchModel(t)
	m.matchIndex = 1
	cmd := m.execute("editor")
	var prepared preparedMsg
	found := false
	for _, msg := range runHistoryCommands(t, cmd) {
		if value, ok := msg.(preparedMsg); ok {
			prepared = value
			found = true
		}
	}
	if !found || prepared.err != nil {
		t.Fatalf("editor preparation failed %+v", prepared)
	}
	if !strings.Contains(strings.Join(prepared.command.Args, " "), "+27") {
		t.Fatalf("selected match lost in editor args %q", prepared.command.Args)
	}
	if m.items["file"].Matches[0].Line != 3 {
		t.Fatal("action reordered stored matches")
	}
}
func TestCustomPreviewActionWorksForHistoryAndChecksExistence(t *testing.T) {
	m, p := liveMatchModel(t)
	m.historical = true
	resolved, err := m.actionSvc.Resolve(m.ctx, *m.current(), m.query.Text)
	if err != nil {
		t.Fatal(err)
	}
	m.resolved = resolved
	result := previewEffect(t, m.execute("fixture-preview"))
	if result.err != nil || !strings.Contains(result.text, "first text") || result.resolved.Preview != "fixture-preview" {
		t.Fatalf("custom preview failed %+v", result)
	}
	_, _ = m.Update(result)
	if !strings.Contains(m.viewport.View(), "first text") || !m.historical {
		t.Fatal("explicit history preview was rejected")
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	result = previewEffect(t, m.execute("fixture-preview"))
	if result.err == nil || !strings.Contains(result.err.Error(), "available") {
		t.Fatalf("history preview did not revalidate: %+v", result)
	}
}
func TestHistoricalCopyRevalidatesAndRejectsStaleReplies(t *testing.T) {
	m, p := liveMatchModel(t)
	m.historical = true
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	var copied copiedMsg
	for _, msg := range runHistoryCommands(t, m.execute("copy")) {
		if c, ok := msg.(copiedMsg); ok {
			copied = c
		}
	}
	if copied.err == nil {
		t.Fatal("missing history path allowed copying")
	}
	_, cmd := m.Update(copied)
	if cmd != nil || !strings.Contains(m.status, "Copy failed") {
		t.Fatal("failed copy wrote clipboard")
	}
	m.pendingAction = true
	m.status = "current"
	_, cmd = m.Update(copiedMsg{gen: m.gen - 1, id: m.selection, value: "stale"})
	if cmd != nil || m.status != "current" || !m.pendingAction {
		t.Fatal("stale copy changed current request")
	}
	_, cmd = m.Update(copiedMsg{gen: m.gen, id: "another", value: "stale"})
	if cmd != nil || m.status != "current" {
		t.Fatal("copy for old selection wrote clipboard")
	}
}
func TestPersistentPreviewInvalidationAfterChildAndHiddenPreview(t *testing.T) {
	m, p := liveMatchModel(t)
	m.cfg.Rules[0].Preview = "fixture-preview"
	// Services are constructed once with the desired effective configuration.
	m.actionSvc = actions.New(m.cfg)
	m.previewSvc = preview.New(m.cfg)
	svc := m.previewSvc
	first := previewEffect(t, m.loadPreview())
	if first.err != nil {
		t.Fatal(first.err)
	}
	_, _ = m.Update(first)
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(p, []byte("other text"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(p, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	_, cmd := m.Update(childMsg{})
	second := previewEffect(t, cmd)
	if second.err != nil || !strings.Contains(second.text, "other text") || m.previewSvc != svc {
		t.Fatalf("preview invalidation failed %+v", second)
	}
	m.cfg.UI.Preview = false
	if err = os.RemoveAll(m.cfg.Paths.Cache); err != nil {
		t.Fatal(err)
	}
	hidden := previewEffect(t, m.loadPreview())
	if hidden.err != nil || !strings.Contains(hidden.text, "Preview hidden") || hidden.resolved.Default == "" {
		t.Fatalf("hidden preview still loaded or actions missing %+v", hidden)
	}
	if _, err = os.Stat(m.cfg.Paths.Cache); !os.IsNotExist(err) {
		t.Fatal("hidden preview wrote cache")
	}
	enabled := previewEffect(t, m.command("preview"))
	if enabled.err != nil || !strings.Contains(enabled.text, "other text") {
		t.Fatalf("enabling preview did not read contents %+v", enabled)
	}
}
func TestMatchKeysRemainPrintableWhileTyping(t *testing.T) {
	m := matchModel(t)
	m.focus = 0
	m.input.Focus()
	m.input.SetValue("")
	_, _ = m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if m.input.Value() != "n" || m.matchIndex != 0 {
		t.Fatal("match key stole text input")
	}
}
