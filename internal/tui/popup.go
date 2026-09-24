package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type popupBounds struct{ x, y, w, h int }

func (r popupBounds) contains(x, y int) bool {
	return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}
func (m *Model) isPopup() bool {
	if m.panel || m.overlay == nil || m.width < 12 || m.height < 8 {
		return false
	}
	switch m.overlay.kind {
	case "actions", "help", "copy", "usage", "delete-history", "completion":
		return true
	}
	return false
}
func (m *Model) popupRect() popupBounds {
	w := min(90, max(40, m.width-4))
	h := min(24, max(8, m.height-4))
	w = min(w, m.width)
	h = min(h, m.height)
	if m.width < 50 || m.height < 14 {
		w = m.width
		h = m.height
	}
	if m.overlay.kind == "completion" {
		h = min(h, max(8, len(m.overlay.choices)+7))
	}
	return popupBounds{(m.width - w) / 2, (m.height - h) / 2, w, h}
}
func (m *Model) popupView() tea.View {
	r := m.popupRect()
	base := *m
	base.overlay = m.overlay.parent
	background := base.View().Content
	panel := *m
	panel.panel = true
	panel.width = r.w - 2
	panel.height = r.h - 2
	lines := panel.overlayLines()
	for len(lines) < panel.height {
		lines = append(lines, "")
	}
	if len(lines) > panel.height {
		lines = lines[:panel.height]
	}
	for i := range lines {
		lines[i] = fit(lines[i], panel.width)
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Render(strings.Join(lines, "\n"))
	content := lipgloss.NewCompositor(lipgloss.NewLayer(background), lipgloss.NewLayer(box).X(r.x).Y(r.y).Z(1)).Render()
	v := tea.NewView(content)
	v.AltScreen = true
	if m.mouse {
		v.MouseMode = tea.MouseModeCellMotion
	}
	return v
}
