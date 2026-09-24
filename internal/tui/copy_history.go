package tui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazyfind/internal/actions"
	"github.com/daviddwlee84/lazyfind/internal/domain"
	"github.com/daviddwlee84/lazyfind/internal/history"
)

func (m *Model) showCopy() tea.Cmd {
	item := m.current()
	if item == nil {
		return nil
	}
	roots := append([]string(nil), m.run.Query.Roots...)
	if len(roots) == 0 {
		roots = append([]string(nil), m.query.Roots...)
	}
	o := &overlay{kind: "copy", title: "Copy — offline references · Enter copies", parent: m.overlay, copyItem: item, copyRoots: roots, copyMatch: m.matchIndex}
	for _, f := range actions.Formats(*item, roots, m.matchIndex) {
		label := f.Label + " → " + f.Value
		if !f.Available {
			label = f.Label + " — " + f.Reason
		}
		o.choices = append(o.choices, choice{label: label, value: f.ID, disabled: !f.Available})
	}
	m.overlay = o
	m.pressed = ""
	return nil
}
func (m *Model) copyChoice(id string) tea.Cmd {
	o := m.overlay
	if o == nil || o.copyItem == nil {
		return nil
	}
	value, err := actions.BuildCopy(*o.copyItem, o.copyRoots, o.copyMatch, id)
	if err != nil {
		m.status = err.Error()
		return nil
	}
	m.overlay = nil
	m.status = "Copied " + domain.Display(value)
	return tea.Batch(m.accept(), tea.SetClipboard(value))
}

type deletedMsg struct {
	gen    int
	id     string
	parent *overlay
	err    error
}

func (m *Model) confirmDelete() tea.Cmd {
	o := m.overlay
	if o == nil || o.kind != "history" {
		return nil
	}
	cs := o.visible()
	if o.selected < 0 || o.selected >= len(cs) {
		return nil
	}
	c := cs[o.selected]
	m.overlay = &overlay{kind: "delete-history", title: "Delete snapshot? " + c.label, parent: o, choices: []choice{{label: "Cancel — keep this history entry", value: "cancel"}, {label: "Delete this snapshot", value: "delete:" + c.value}}}
	m.pressed = ""
	return nil
}
func (m *Model) deleteHistory(id string, parent *overlay) tea.Cmd {
	cfg := m.cfg
	gen := m.historyGen
	m.overlay = parent
	m.status = "Deleting history entry…"
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := history.New(cfg).Delete(ctx, id)
		return deletedMsg{gen, id, parent, err}
	}
}
func (m *Model) historyDeleted(msg deletedMsg) tea.Cmd {
	if msg.gen != m.historyGen {
		return nil
	}
	if msg.err != nil {
		m.status = "Delete failed: " + msg.err.Error()
		return nil
	}
	m.status = "History entry deleted"
	if msg.id == m.run.ID {
		m.accepted = false
	}
	o := m.overlay
	if o != nil && o.kind == "history" {
		cs := o.choices[:0]
		for _, c := range o.choices {
			if c.value != msg.id {
				cs = append(cs, c)
			}
		}
		o.choices = cs
		rs := o.runs[:0]
		for _, r := range o.runs {
			if r.ID != msg.id {
				rs = append(rs, r)
			}
		}
		o.runs = rs
		o.selected = min(o.selected, max(0, len(o.visible())-1))
		return m.historyList()
	}
	return nil
}
func (m *Model) historyFooterAction(action string) tea.Cmd {
	switch action {
	case "delete":
		return m.confirmDelete()
	case "pin":
		return m.overlayKey(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	case "rerun":
		return m.overlayKey(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	}
	return nil
}
func (m *Model) usageHint() string {
	return fmt.Sprintf("Includes hidden/ignored • no symlink following • %d concurrent • %ds per directory", m.cfg.DirectoryUsage.Concurrency, m.cfg.DirectoryUsage.TimeoutSeconds)
}
