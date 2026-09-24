package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazyfind/internal/actions"
	"github.com/daviddwlee84/lazyfind/internal/config"
	"github.com/daviddwlee84/lazyfind/internal/domain"
	"github.com/daviddwlee84/lazyfind/internal/usage"
)

func TestStartupFocusEmptyPolicyAndExplicitSubmit(t *testing.T) {
	cfg := config.Defaults()
	cfg.UI.InitialFocus = "results"
	cfg.Search.AutoSearchEmpty = false
	cfg.History.Enabled = false
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := New(ctx, cfg, domain.QuerySpec{}, false)
	m.Init()
	if m.focus != 1 || m.input.Focused() || m.searching || m.cancel != nil {
		t.Fatal("idle startup performed search or stole focus")
	}
	m.key(tea.KeyPressMsg{Code: 'i', Text: "i"})
	if m.focus != 0 || !m.input.Focused() {
		t.Fatal("i did not enter query")
	}
	m.key(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.searching {
		t.Fatal("Tab bypassed empty-query policy")
	}
	m.key(tea.KeyPressMsg{Code: 'i', Text: "i"})
	m.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.searching || !m.accepted {
		t.Fatal("explicit Enter did not list empty query")
	}
	m.cancel()
	cfg.UI.InitialFocus = "search"
	cfg.Search.AutoSearchEmpty = true
	m = New(ctx, cfg, domain.QuerySpec{}, false)
	m.Init()
	if m.focus != 0 || !m.input.Focused() || !m.searching {
		t.Fatal("existing defaults changed")
	}
	m.cancel()
}
func TestConditionalOnlyQueryIsNotEmptyAndClearingCancels(t *testing.T) {
	m := historyModel(t)
	m.cfg.Search.AutoSearchEmpty = false
	m.input.SetValue("ext:md")
	m.start(false)
	if !m.searching || len(m.query.Filters.Extensions) != 1 {
		t.Fatal("qualifier-only query did not run")
	}
	oldGen := m.gen
	m.input.SetValue("")
	m.queryEdited("ext:md", nil)
	if m.searching || m.gen <= oldGen || len(m.items) > 0 {
		t.Fatal("cleared query did not retire work")
	}
}
func TestCompletionAppendsToKeywordAndPreservesLiteral(t *testing.T) {
	m := historyModel(t)
	m.query.Target.Host = "offline"
	m.input.SetValue("needle")
	m.input.CursorEnd()
	m.showCompletion(true)
	if len(m.overlay.choices) == 0 {
		t.Fatal("no discoverable conditions after keyword")
	}
	for n, c := range m.overlay.choices {
		if strings.TrimSpace(c.value) == "ext:" {
			m.overlay.selected = n
			break
		}
	}
	m.acceptCompletion()
	if m.input.Value() != "needle ext:" || m.overlay == nil {
		t.Fatalf("%q", m.input.Value())
	}
	for n, c := range m.overlay.choices {
		if c.value == "ext:md" {
			m.overlay.selected = n
			break
		}
	}
	m.acceptCompletion()
	if m.input.Value() != "needle ext:md " || m.overlay != nil {
		t.Fatalf("%q", m.input.Value())
	}
	q, err := domain.ParseQuery(m.input.Value(), m.query)
	if err != nil || q.Text != "needle" || q.Filters.Extensions[0] != "md" {
		t.Fatal(q, err)
	}
	m.input.SetValue(`"literal type:dir"`)
	m.input.CursorEnd()
	m.showCompletion(true)
	m.overlay.selected = 0
	m.acceptCompletion()
	if !strings.HasPrefix(m.input.Value(), `"literal type:dir" type:`) {
		t.Fatal("completion replaced literal query")
	}
}
func TestPopupIsolationGenerationsAndNestedResponses(t *testing.T) {
	m := historyModel(t)
	m.historical = true
	m.showActions()
	old := m.actionsGen
	m.overlay = nil
	m.showActions()
	_, _ = m.Update(actionsMsg{gen: m.gen, dialog: old, id: m.selection, resolved: actions.Resolved{Default: "stale"}})
	if m.resolved.Default == "stale" {
		t.Fatal("reopened actions accepted old reply")
	}
	parent := m.overlay
	m.showCopy()
	_, _ = m.Update(actionsMsg{gen: m.gen, dialog: m.actionsGen, id: m.selection, resolved: actions.Resolved{Actions: []actions.ResolvedAction{{Action: config.Action{ID: "fresh", Label: "Fresh"}, Available: true}}}})
	if m.overlay.kind != "copy" || parent.choices[len(parent.choices)-1].value != "fresh" {
		t.Fatal("nested popup lost its parent's response")
	}
	r := m.popupRect()
	if r.x > 0 && m.hit(r.x-1, r.y+2) != "" {
		t.Fatal("outside click targets background")
	}
	m.overlayKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.overlay != parent {
		t.Fatal("Back did not restore parent")
	}
}
func TestVisibleUsageSnapshotAndIndependentSizeSorting(t *testing.T) {
	m := historyModel(t)
	m.height = 12
	m.items = map[string]domain.Item{}
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		i := domain.Item{ID: name, Name: name, Path: "/" + name, Kind: "directory"}
		m.items[name] = i
	}
	m.rebuild()
	m.offset = 1
	m.selected = 1
	m.selection = "b"
	m.showUsage()
	if len(m.overlay.usageVisible) != 3 || m.overlay.usageVisible[0].ID != "b" {
		t.Fatal("visible measurement did not snapshot viewport")
	}
	m.offset = 0
	if m.overlay.usageVisible[0].ID != "b" {
		t.Fatal("scroll changed the pending targets")
	}
	m.overlay = nil
	m.usageValues["a"] = usage.Result{Status: "complete", Bytes: 200}
	m.usageValues["b"] = usage.Result{Status: "complete", Bytes: 100}
	m.sort = "usage"
	m.rebuild()
	if m.rows[0].ID != "b" || m.selection != "b" || m.items["b"].Size != nil {
		t.Fatal("usage sorting changed identity or persisted file size")
	}
	old := m.usageGen
	m.cancelUsage()
	m.receiveUsage(usageMsg{gen: old, result: &usage.Result{ItemID: "c", Status: "complete", Bytes: 50}})
	if _, ok := m.usageValues["c"]; ok {
		t.Fatal("late usage entered current view")
	}
}
func TestHistoryDeleteDefaultsCancelAndPreservesFilter(t *testing.T) {
	m := historyModel(t)
	o := &overlay{kind: "history", input: field("Find history", "needle"), choices: []choice{{label: "needle record", value: "run-one"}}}
	m.overlay = o
	m.confirmDelete()
	if m.overlay.selected != 0 || m.overlay.choices[0].value != "cancel" {
		t.Fatal("delete did not default to cancel")
	}
	m.activateChoice()
	if m.overlay != o || o.input.Value() != "needle" {
		t.Fatal("cancel lost history context")
	}
}
func TestListFilterDoesNotChangeSearchGeneration(t *testing.T) {
	m := filterModel(t)
	gen := m.gen
	edit := m.edit
	m.Update(tea.KeyPressMsg{Code: 'b', Text: "b"})
	if m.gen != gen || m.edit != edit || m.selection != "beta" {
		t.Fatal("current-list filtering restarted retrieval")
	}
}
