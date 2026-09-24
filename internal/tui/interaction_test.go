package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazyfind/internal/actions"
	"github.com/daviddwlee84/lazyfind/internal/domain"
)

func TestRootListFocusDoesNotIndexNegativeFieldOnAsyncInput(t *testing.T) {
	m := historyModel(t)
	m.overlay = &overlay{kind: "roots", fields: []textinput.Model{field("Host", "fixture"), field("Root", "~")}, field: -1}
	_, _ = m.Update(tea.PasteMsg{Content: "ignored while list focused"})
	if m.overlay.fields[0].Value() != "fixture" || m.overlay.fields[1].Value() != "~" {
		t.Fatal("list input changed a field")
	}
	_, _ = m.Update(struct{}{})
}

func TestFilterFormPreservesUnsubmittedKeywordAndInlineFilters(t *testing.T) {
	m := historyModel(t)
	m.query.Target.Host = "offline"
	m.query.Text = "old keyword"
	m.input.SetValue("new keyword ext:md")
	_ = m.showFilters()
	if m.overlay.fields[1].Value() != "md" {
		t.Fatal("form ignored draft qualifier")
	}
	m.overlay.fields[0].SetValue("dir")
	_ = m.applyFields()
	if m.query.Text != "new keyword" || m.query.BaseFilters == nil || len(m.query.Filters.Kinds) != 1 || m.query.Filters.Kinds[0] != "directory" || !m.dirty || m.searching {
		t.Fatalf("draft was lost %+v", m.query)
	}
	parsed, err := domain.ParseQuery(m.input.Value(), m.query)
	if err != nil || parsed.Text != "new keyword" || len(parsed.Filters.Extensions) != 1 || parsed.Filters.Extensions[0] != "md" {
		t.Fatalf("form and text drifted %+v %v", parsed, err)
	}
}
func TestFilterFormCanDisableBadRegexAndDoesNotCommitInvalidEdit(t *testing.T) {
	m := historyModel(t)
	m.query.Target.Host = "offline"
	m.query.Regex = true
	m.input.SetValue("[")
	_ = m.showFilters()
	m.overlay.fields[10].SetValue("false")
	_ = m.applyFields()
	if m.query.Regex || m.query.Text != "[" || m.overlay != nil {
		t.Fatal("could not recover from invalid regex")
	}
	_ = m.showFilters()
	m.overlay.fields[10].SetValue("true")
	_ = m.applyFields()
	if m.query.Regex || m.overlay == nil || !strings.Contains(m.status, "invalid regex") {
		t.Fatal("invalid filter form mutated the active query")
	}
}
func TestNewGenerationRetiresPendingAction(t *testing.T) {
	m := historyModel(t)
	m.pendingAction = true
	oldGen := m.gen
	_ = m.start(false)
	if m.pendingAction || m.gen == oldGen {
		t.Fatal("new query retained previous pending action")
	}
	_, _ = m.Update(preparedMsg{gen: oldGen, id: "one"})
	if m.pendingAction {
		t.Fatal("stale preparation reactivated a canceled action")
	}
	m.pendingAction = true
	_, _ = m.Update(preparedMsg{gen: oldGen, id: "one"})
	if !m.pendingAction {
		t.Fatal("stale preparation canceled a new action")
	}
}
func filterModel(t *testing.T) *Model {
	m := historyModel(t)
	m.searching = false
	m.focus = 3
	m.input.Blur()
	m.filter.Focus()
	m.items = map[string]domain.Item{"alpha": {ID: "alpha", Name: "alpha.txt", Path: "/alpha.txt", Kind: "file"}, "beta": {ID: "beta", Name: "beta", Path: "/beta", Kind: "directory"}}
	m.selection = "alpha"
	m.rebuild()
	m.resolved = actions.Resolved{Default: "editor"}
	return m
}
func TestFuzzyFilterRefreshesPreviewAndClearsOldActions(t *testing.T) {
	m := filterModel(t)
	oldGen := m.previewGen
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'b', Text: "b"})
	if m.selection != "beta" || m.previewGen <= oldGen || m.resolved.Default != "" || cmd == nil {
		t.Fatalf("stale preview/actions: selection=%s gen=%d default=%s", m.selection, m.previewGen, m.resolved.Default)
	}
}
func TestPasteUpdatesQueryDebounceAndResultFilter(t *testing.T) {
	m := historyModel(t)
	m.dirty = false
	m.input.SetValue("")
	oldEdit := m.edit
	_, cmd := m.Update(tea.PasteMsg{Content: "needle"})
	if m.input.Value() != "needle" || !m.dirty || m.edit <= oldEdit || cmd == nil {
		t.Fatal("pasted query did not schedule search")
	}
	m = filterModel(t)
	oldPreview := m.previewGen
	_, _ = m.Update(tea.PasteMsg{Content: "beta"})
	if m.selection != "beta" || m.previewGen <= oldPreview || m.resolved.Default != "" {
		t.Fatal("pasted result filter left stale selection preview")
	}
	m = historyModel(t)
	m.query.Target.Host = "offline"
	m.dirty = false
	m.input.SetValue("")
	oldGen := m.gen
	_, _ = m.Update(tea.PasteMsg{Content: "remote draft"})
	if !m.dirty || m.gen != oldGen || m.query.Text == "remote draft" {
		t.Fatal("remote paste executed before submission")
	}
}
func TestLateRootDiscoveryCannotReplaceReopenedPicker(t *testing.T) {
	m := historyModel(t)
	_ = m.showRoots()
	oldGen := m.rootsGen
	m.overlay = nil
	_ = m.showRoots()
	_, _ = m.Update(rootsMsg{gen: oldGen, choices: []choice{{label: "stale", value: "bad"}}})
	if len(m.overlay.choices) != 0 {
		t.Fatal("stale roots replaced new picker")
	}
	_, _ = m.Update(rootsMsg{gen: m.rootsGen, choices: []choice{{label: "fresh", value: "good"}}})
	if len(m.overlay.choices) != 1 || m.overlay.choices[0].label != "fresh" {
		t.Fatal("current discovery ignored")
	}
}
func TestCanceledFullEventBufferStillReceivesCompletion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ch := make(chan domain.Event, 2)
	ch <- domain.Event{Kind: "item"}
	ch <- domain.Event{Kind: "problem"}
	finished := make(chan struct{})
	go func() {
		finishEvents(ctx, ch, domain.Event{Kind: "done", Run: &domain.Run{Status: "cancelled"}})
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("canceled stream worker blocked")
	}
	found := false
	for len(ch) > 0 {
		if (<-ch).Kind == "done" {
			found = true
		}
	}
	if !found {
		t.Fatal("cancellation left model searching forever")
	}
}
func TestNativeSSHChoiceAvailableBeforeDiscoveryAndDoesNotSearch(t *testing.T) {
	m := historyModel(t)
	m.query.Target.Host = "fixture"
	m.searching = false
	_ = m.showRoots()
	if len(m.overlay.choices) != 1 || m.overlay.choices[0].value != "ssh-session:fixture" {
		t.Fatal("native SSH missing before discovery")
	}
	oldGen := m.gen
	m.overlay.field = -1
	m.overlay.selected = 0
	cmd := m.activateChoice()
	if cmd == nil || m.overlay != nil || !m.pendingAction || m.gen != oldGen || m.searching {
		t.Fatal("native SSH handoff did not preserve search state")
	}
}
