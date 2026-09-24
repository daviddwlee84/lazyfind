package tui

import (
	"context"
	"fmt"
	"sort"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazyfind/internal/domain"
	"github.com/daviddwlee84/lazyfind/internal/usage"
)

type usageMsg struct {
	gen     int
	result  *usage.Result
	done    bool
	channel <-chan usageMsg
}

func isDir(i domain.Item) bool { return i.Kind == "dir" || i.Kind == "directory" }
func (m *Model) showUsage() tea.Cmd {
	o := &overlay{kind: "usage", title: "Disk usage · recursive; may be slow", parent: m.overlay}
	if i := m.current(); i != nil && isDir(*i) {
		o.usageItems = []domain.Item{*i}
	}
	seen := map[string]bool{}
	for n := m.offset; n < min(len(m.rows), m.offset+m.layout().bodyHeight); n++ {
		i := m.rows[n]
		if isDir(i) && !seen[i.ID] {
			seen[i.ID] = true
			o.usageVisible = append(o.usageVisible, i)
		}
	}
	o.choices = []choice{
		{label: fmt.Sprintf("Calculate selected directory (%d)", len(o.usageItems)), value: "selected", disabled: len(o.usageItems) == 0},
		{label: fmt.Sprintf("Calculate visible directories (%d)", len(o.usageVisible)), value: "visible", disabled: len(o.usageVisible) == 0},
		{label: "Recalculate selected (ignore session cache)", value: "refresh-selected", disabled: len(o.usageItems) == 0},
		{label: "Recalculate visible (ignore session cache)", value: "refresh-visible", disabled: len(o.usageVisible) == 0},
	}
	if m.usageRunning {
		o.choices = append(o.choices, choice{label: "Cancel current measurement", value: "cancel"})
	}
	m.overlay = o
	m.pressed = ""
	return nil
}
func (m *Model) cancelUsage() {
	if m.usageCancel != nil {
		m.usageCancel()
		m.usageCancel = nil
	}
	if m.usageRunning {
		for id, r := range m.usageValues {
			if r.Status == "running" {
				r.Status = "canceled"
				m.usageValues[id] = r
			}
		}
	}
	m.usageGen++
	m.usageRunning = false
}
func (m *Model) startUsage(items []domain.Item, force bool) tea.Cmd {
	if len(items) == 0 {
		return nil
	}
	items = append([]domain.Item(nil), items...)
	m.cancelUsage()
	gen := m.usageGen
	m.usageTotal = len(items)
	m.usageDone = 0
	m.usageRunning = true
	m.overlay = nil
	m.focus = 1
	m.input.Blur()
	ctx, cancel := context.WithCancel(m.ctx)
	m.usageCancel = cancel
	for _, i := range items {
		m.usageValues[i.ID] = usage.Result{ItemID: i.ID, Status: "running"}
	}
	svc := m.usageSvc
	ch := make(chan usageMsg, len(items)+1)
	go func() {
		defer close(ch)
		svc.MeasureBatch(ctx, items, force, func(result usage.Result) { r := result; ch <- usageMsg{gen: gen, result: &r} })
		ch <- usageMsg{gen: gen, done: true}
	}()
	m.status = fmt.Sprintf("Measuring disk usage: 0/%d · Esc cancels", len(items))
	return waitUsage(ch)
}
func waitUsage(ch <-chan usageMsg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		msg.channel = ch
		return msg
	}
}
func (m *Model) receiveUsage(msg usageMsg) tea.Cmd {
	if msg.gen != m.usageGen {
		return nil
	}
	if msg.result != nil {
		m.usageValues[msg.result.ItemID] = *msg.result
		m.usageDone++
	}
	if msg.done {
		m.usageRunning = false
		m.usageCancel = nil
	}
	m.status = fmt.Sprintf("Disk usage %d/%d · session measurements", m.usageDone, m.usageTotal)
	if m.usageRunning {
		m.status += " · Esc cancels"
	}
	m.rebuild()
	m.refreshPreview()
	if !msg.done && msg.channel != nil {
		return waitUsage(msg.channel)
	}
	return nil
}
func (m *Model) usageDescription(r usage.Result) string {
	if r.Status == "running" {
		return "Disk usage: measuring… (recursive; includes hidden/ignored; no symlink following)"
	}
	if r.Status != "complete" {
		return fmt.Sprintf("Disk usage: %s · %s", r.Status, r.Error)
	}
	return fmt.Sprintf("Disk usage: %s · measured %s · session cache; u to recalculate", domain.HumanSize(&r.Bytes), r.MeasuredAt.Local().Format(time.RFC3339))
}
func (m *Model) usageCell(id string) string {
	r, ok := m.usageValues[id]
	if !ok {
		return "—"
	}
	if r.Status == "complete" {
		return domain.HumanSize(&r.Bytes)
	}
	return r.Status
}
func (m *Model) sortRows() {
	if m.sort != "usage" {
		domain.SortItems(m.rows, m.sort, m.desc)
		return
	}
	sort.SliceStable(m.rows, func(a, b int) bool {
		x, y := m.usageValues[m.rows[a].ID], m.usageValues[m.rows[b].ID]
		xc, yc := x.Status == "complete", y.Status == "complete"
		if xc != yc {
			return xc
		}
		if !xc || x.Bytes == y.Bytes {
			return m.rows[a].ID < m.rows[b].ID
		}
		if m.desc {
			return x.Bytes > y.Bytes
		}
		return x.Bytes < y.Bytes
	})
}
