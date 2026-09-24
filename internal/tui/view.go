package tui

import (
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazyfind/internal/domain"
)

type geometry struct {
	width, height, listWidth, bodyY, bodyHeight, nameWidth int
	split                                                  bool
	usage, modified                                        bool
}

func (m *Model) layout() geometry {
	w, h := max(1, m.width), max(1, m.height)
	split := m.cfg.UI.Preview && w >= 110
	lw := w
	if split {
		lw = w * 3 / 5
	}
	showUsage := len(m.usageValues) > 0
	modified := !showUsage || lw >= 88
	reserved := 17
	if modified {
		reserved += 19
	}
	if showUsage {
		reserved += 11
	}
	nw := max(8, lw-reserved)
	return geometry{width: w, height: h, listWidth: lw, bodyY: 6, bodyHeight: max(1, h-9), nameWidth: nw, split: split, usage: showUsage, modified: modified}
}
func (m *Model) resize() {
	g := m.layout()
	m.input.SetWidth(max(1, m.width-10))
	m.filter.SetWidth(max(1, m.width-20))
	pw := m.width
	if g.split {
		pw = m.width - g.listWidth - 3
	}
	m.viewport.SetWidth(max(1, pw))
	m.viewport.SetHeight(g.bodyHeight)
	m.clamp()
}
func fit(s string, w int) string {
	w = max(0, w)
	s = ansi.Truncate(s, w, "…")
	return s + strings.Repeat(" ", max(0, w-ansi.StringWidth(s)))
}
func (m *Model) accent(s string) string {
	if m.cfg.UI.Color == "never" || (m.cfg.UI.Color != "always" && os.Getenv("NO_COLOR") != "") {
		return s
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true).Render(s)
}
func (m *Model) colors() bool {
	return m.cfg.UI.Color != "never" && (m.cfg.UI.Color == "always" || os.Getenv("NO_COLOR") == "")
}
func (m *Model) selectedStyle(s string) string {
	if m.cfg.UI.Color == "never" || (m.cfg.UI.Color != "always" && os.Getenv("NO_COLOR") != "") {
		return s
	}
	return lipgloss.NewStyle().Reverse(true).Render(s)
}
func (m *Model) View() tea.View {
	if m.isPopup() {
		return m.popupView()
	}
	g := m.layout()
	var lines []string
	if m.overlay != nil {
		lines = m.overlayLines()
	} else {
		title := " lazyfind  /  find → inspect → act"
		if m.pick {
			title += "  [picker]"
		}
		if m.historical {
			title += "  [HISTORY SNAPSHOT]"
		}
		lines = append(lines, m.accent(fit(title, g.width)), " Search  "+m.input.View(), " Scope   "+domain.Display(rootLabel(m.query))+"  ["+m.cfg.Keymap["roots"]+" roots]", m.sourceLine())
		if m.focus == 3 || m.filter.Value() != "" {
			lines = append(lines, m.filter.View()+fmt.Sprintf("  %d/%d loaded · no rescan", len(m.rows), len(m.items)))
		} else {
			mode := "literal · smart case"
			if m.query.Regex {
				mode = "regex · smart case"
			}
			path := "names"
			if m.query.FullPath {
				path = "full paths"
			}
			lines = append(lines, fmt.Sprintf(" Match   %s · %s · %s", mode, path, m.filterSummary()))
		}
		header := fit("  Name / path", g.nameWidth) + fit("Kind", 7) + fit("Size", 10)
		if g.usage {
			header += fit("Disk usage", 11)
		}
		if g.modified {
			header += "Modified"
		}
		if g.split {
			header = fit(header, g.listWidth) + " │ Preview · " + m.cfg.Keymap["next_match"] + "/" + m.cfg.Keymap["previous_match"] + " matches"
		}
		if m.focus == 2 && !g.split {
			header = " Preview — Tab returns to query/results"
		}
		lines = append(lines, m.accent(header))
		pv := strings.Split(m.viewport.View(), "\n")
		for y := 0; y < g.bodyHeight; y++ {
			line := ""
			n := m.offset + y
			if n < len(m.rows) {
				i := m.rows[n]
				mark := "  "
				if n == m.selected {
					mark = "> "
				}
				path := i.RawPath()
				for _, root := range m.query.Roots {
					if strings.HasPrefix(path, strings.TrimSuffix(root, "/")+"/") {
						path = strings.TrimPrefix(path, strings.TrimSuffix(root, "/")+"/")
						break
					}
				}
				mt := "—"
				if i.Modified != nil {
					mt = i.Modified.Local().Format("2006-01-02 15:04")
				}
				line = fit(mark+m.pathText(i, path, n == m.selected), g.nameWidth) + fit(i.Kind, 7) + fit(domain.HumanSize(i.Size), 10)
				if g.usage {
					line += fit(m.usageCell(i.ID), 11)
				}
				if g.modified {
					line += mt
				}
				line = fit(line, g.listWidth)
				if n == m.selected {
					// Colorize non-match segments separately so yellow survives selection.
					tail := fit(i.Kind, 7) + fit(domain.HumanSize(i.Size), 10)
					if g.usage {
						tail += fit(m.usageCell(i.ID), 11)
					}
					if g.modified {
						tail += mt
					}
					line = fit(mark+m.pathText(i, path, true), g.nameWidth) + m.selectedStyle(fit(tail, max(0, g.listWidth-g.nameWidth)))
				}
			} else if y == 0 {
				line = "  Choose roots → keyword / conditions → Ctrl+F refine → : act"
			}
			if g.split {
				pr := ""
				if y < len(pv) {
					pr = pv[y]
				}
				line = fit(line, g.listWidth) + " │ " + pr
			} else if m.focus == 2 {
				line = ""
				if y < len(pv) {
					line = pv[y]
				}
			}
			lines = append(lines, line)
		}
		state := m.status
		if m.dirty {
			state = "Draft · " + state
		}
		if m.searching {
			state = fmt.Sprintf("Searching · %d results · Esc cancel", len(m.items))
		}
		if m.run.Truncated || m.run.SnapshotTruncated {
			state += " · TRUNCATED"
		}
		if m.pendingG {
			state += " · g…"
		}
		if m.usageRunning {
			state += fmt.Sprintf(" · du %d/%d (Esc cancels)", m.usageDone, m.usageTotal)
		}
		lines = append(lines, domain.Display(state))
		enter := "actions"
		for _, a := range m.resolved.Actions {
			if a.ID == m.resolved.Default {
				enter = a.Label
			}
		}
		if m.focus == 0 {
			enter = "accept query"
		}
		if m.pick && m.focus == 1 {
			enter = "select"
		}
		lines = append(lines, fmt.Sprintf(" Enter %s  %s search  %s filters  %s roots  %s sort  %s history", enter, m.cfg.Keymap["search"], m.cfg.Keymap["filters"], m.cfg.Keymap["roots"], m.cfg.Keymap["sort"], m.cfg.Keymap["history"]), fmt.Sprintf(" %s conditions  %s list filter  %s actions  %s copy  %s usage  %s help  %s quit", m.cfg.Keymap["complete_query"], m.cfg.Keymap["result_filter"], m.cfg.Keymap["actions"], m.cfg.Keymap["copy_menu"], m.cfg.Keymap["directory_usage"], m.cfg.Keymap["help"], m.cfg.Keymap["quit"]))
	}
	for len(lines) < g.height {
		lines = append(lines, "")
	}
	if len(lines) > g.height {
		lines = lines[:g.height]
	}
	for i := range lines {
		lines[i] = fit(lines[i], g.width)
	}
	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	if m.mouse {
		v.MouseMode = tea.MouseModeCellMotion
	}
	return v
}
func (m *Model) sourceLine() string {
	s := " Sources "
	for _, src := range []string{"names", "text", "documents", "recent"} {
		mark := " "
		if contains(m.query.Sources, src) {
			mark = "x"
		}
		s += "[" + mark + " " + src + "] "
	}
	return s
}
func (m *Model) filterSummary() string {
	f := m.query.Filters
	var s []string
	if len(f.Kinds) > 0 {
		s = append(s, "type:"+strings.Join(f.Kinds, ","))
	}
	if len(f.Extensions) > 0 {
		s = append(s, "ext:"+strings.Join(f.Extensions, ","))
	}
	if f.ModifiedWithin != "" {
		s = append(s, "mtime:<"+f.ModifiedWithin)
	}
	if f.After != "" {
		s = append(s, "after:"+f.After)
	}
	if f.Before != "" {
		s = append(s, "before:"+f.Before)
	}
	if f.MinSize != nil {
		s = append(s, "size>="+domain.HumanSize(f.MinSize))
	}
	if f.MaxSize != nil {
		s = append(s, "size<="+domain.HumanSize(f.MaxSize))
	}
	if f.Hidden {
		s = append(s, "hidden")
	}
	if f.Ignored {
		s = append(s, "ignored")
	}
	if f.Depth > 0 {
		s = append(s, fmt.Sprintf("depth:%d", f.Depth))
	}
	if len(s) == 0 {
		return "[" + m.cfg.Keymap["filters"] + " filters]"
	}
	return strings.Join(s, " ")
}
func (m *Model) overlayListY() int {
	o := m.overlay
	if o == nil {
		return 3
	}
	if len(o.fields) > 0 {
		return min(3+len(o.fields)+1, max(3, m.height-5))
	}
	if o.kind == "actions" || o.kind == "history" || o.kind == "help" {
		return 4
	}
	return 3
}
func (m *Model) overlayLines() []string {
	o := m.overlay
	lines := []string{m.accent(" lazyfind / " + o.kind), m.accent(" " + o.title), ""}
	if o.kind == "usage" {
		lines[2] = m.usageHint()
	}
	if len(o.fields) > 0 {
		visible := max(1, m.overlayListY()-4)
		start := max(0, o.field-visible+1)
		for i := start; i < min(len(o.fields), start+visible); i++ {
			mark := "  "
			if o.field == i {
				mark = "> "
			}
			f := o.fields[i]
			f.SetWidth(max(1, m.width-ansi.StringWidth(f.Prompt)-4))
			if o.kind == "filters" && i >= 8 {
				check := "[ ] "
				if f.Value() == "true" {
					check = "[x] "
				}
				lines = append(lines, mark+check+strings.TrimSuffix(f.Prompt, ": "))
			} else {
				lines = append(lines, mark+f.View())
			}
		}
		for len(lines) < m.overlayListY() {
			lines = append(lines, "")
		}
	}
	if o.kind == "actions" || o.kind == "history" || o.kind == "help" {
		f := o.input
		f.SetWidth(max(1, m.width-18))
		lines = append(lines, f.View())
	}
	cs := o.visible()
	capacity := max(1, m.height-len(lines)-3)
	start := max(0, o.selected-capacity+1)
	if len(cs) == 0 && o.kind != "filters" {
		lines = append(lines, "  No entries (or still loading).")
	}
	for n := start; n < min(len(cs), start+capacity); n++ {
		c := cs[n]
		mark := "  "
		if n == o.selected {
			mark = "> "
		}
		checked := ""
		if o.kind == "sources" || o.kind == "sort" {
			checked = "[ ] "
			if c.checked {
				checked = "[x] "
			}
		}
		s := mark + checked + domain.Display(c.label)
		if n == o.selected {
			s = m.selectedStyle(s)
		}
		lines = append(lines, s)
	}
	for len(lines) < m.height-2 {
		lines = append(lines, "")
	}
	footer := "[ Apply ] [ Cancel ]  Enter chooses · Esc back"
	if o.kind == "history" {
		footer = "[Delete] [Pin] [Re-run] [Close]  Ctrl+D / Ctrl+P / Ctrl+R"
	}
	if o.kind == "help" {
		footer = "[ Close ]  Type to find · arrows/wheel scroll"
	}
	return append(lines, domain.Display(m.status), footer)
}
func (m *Model) overlayApplyKey() string {
	o := m.overlay
	key := "overlay-apply:" + o.kind
	for _, f := range o.fields {
		key += "\x00" + f.Value()
	}
	for _, c := range o.choices {
		if c.checked {
			key += "\x00" + c.value
		}
	}
	cs := o.visible()
	if o.selected >= 0 && o.selected < len(cs) {
		key += "\x00" + cs[o.selected].value
	}
	return key
}
func (m *Model) hit(x, y int) string {
	if x < 0 || y < 0 || x >= m.width || y >= m.height {
		return ""
	}
	if m.isPopup() {
		r := m.popupRect()
		if !r.contains(x, y) || x == r.x || y == r.y || x == r.x+r.w-1 || y == r.y+r.h-1 {
			return ""
		}
		panel := *m
		panel.panel = true
		panel.width = r.w - 2
		panel.height = r.h - 2
		return panel.hit(x-r.x-1, y-r.y-1)
	}
	if o := m.overlay; o != nil {
		if y == m.height-1 {
			if o.kind == "history" {
				if x < 8 {
					return "history:delete"
				}
				if x < 14 {
					return "history:pin"
				}
				if x < 23 {
					return "history:rerun"
				}
				if x < 30 {
					return "overlay-cancel:history"
				}
			}
			if o.kind == "help" && x < 9 {
				return "overlay-cancel:help"
			}
		}
		if o.kind != "help" && y == m.height-1 {
			if x < 9 {
				return m.overlayApplyKey()
			}
			if x >= 10 && x < 20 {
				return "overlay-cancel:" + o.kind
			}
		}
		if len(o.fields) > 0 {
			visible := max(1, m.overlayListY()-4)
			start := max(0, o.field-visible+1)
			if y >= 3 && y < 3+visible && start+y-3 < len(o.fields) {
				return fmt.Sprintf("field:%d", start+y-3)
			}
		}
		cs := o.visible()
		ly := m.overlayListY()
		capacity := max(1, m.height-ly-3)
		start := max(0, o.selected-capacity+1)
		n := start + y - ly
		if y >= ly && y < m.height-3 && n < len(cs) && n >= 0 {
			return "choice:" + cs[n].value
		}
		return ""
	}
	g := m.layout()
	if y == 1 {
		return "query"
	}
	if y == 2 {
		return "roots"
	}
	if y == 4 {
		return "filters"
	}
	if y == 3 {
		off := 9
		for _, s := range []string{"names", "text", "documents", "recent"} {
			w := len(s) + 4
			if x >= off && x < off+w {
				return "source:" + s
			}
			off += w + 1
		}
	}
	if y == 5 {
		if m.focus == 2 && !g.split {
			return "preview"
		}
		if x < g.nameWidth {
			return "sort:name"
		}
		if x < g.nameWidth+7 {
			return "sort:kind"
		}
		if x < g.nameWidth+17 {
			return "sort:size"
		}
		if g.usage && x < g.nameWidth+28 {
			return "sort:usage"
		}
		if x < g.listWidth {
			return "sort:modified"
		}
	}
	if y >= g.bodyY && y < g.bodyY+g.bodyHeight {
		if (g.split && x > g.listWidth) || (!g.split && m.focus == 2) {
			return "preview"
		}
		n := m.offset + y - g.bodyY
		if n < len(m.rows) {
			return "row:" + m.rows[n].ID
		}
	}
	return ""
}
func (m *Model) mouseClick(v tea.Mouse) tea.Cmd {
	if !m.mouse || v.Button != tea.MouseLeft {
		return nil
	}
	m.pressed = m.hit(v.X, v.Y)
	return nil
}
func (m *Model) mouseRelease(v tea.Mouse) tea.Cmd {
	if !m.mouse {
		return nil
	}
	h := m.hit(v.X, v.Y)
	pressed := m.pressed
	m.pressed = ""
	if pressed == "" || pressed != h {
		return nil
	}
	if m.overlay != nil {
		if strings.HasPrefix(h, "history:") {
			return m.historyFooterAction(strings.TrimPrefix(h, "history:"))
		}
		if strings.HasPrefix(h, "overlay-cancel:") {
			m.overlay = m.overlay.parent
			return nil
		}
		if strings.HasPrefix(h, "overlay-apply:") {
			if len(m.overlay.fields) > 0 && m.overlay.field >= 0 {
				return m.applyFields()
			}
			if m.overlay.kind == "delete-history" {
				return m.overlayKey(tea.KeyPressMsg{Code: tea.KeyEnter})
			}
			return m.activateChoice()
		}
		if strings.HasPrefix(h, "field:") {
			var n int
			fmt.Sscanf(h, "field:%d", &n)
			if m.overlay.field >= 0 {
				m.overlay.fields[m.overlay.field].Blur()
			}
			m.overlay.field = n
			if m.overlay.kind == "filters" && n >= 8 {
				f := &m.overlay.fields[n]
				if f.Value() == "true" {
					f.SetValue("false")
				} else {
					f.SetValue("true")
				}
			}
			return m.overlay.fields[n].Focus()
		}
		if strings.HasPrefix(h, "choice:") {
			val := strings.TrimPrefix(h, "choice:")
			for n, c := range m.overlay.visible() {
				if c.value == val {
					m.overlay.selected = n
					break
				}
			}
			if m.overlay.kind == "sources" {
				for n, c := range m.overlay.choices {
					if c.value == val {
						m.overlay.choices[n].checked = !c.checked
					}
				}
				return nil
			}
			return m.activateChoice()
		}
		return nil
	}
	switch h {
	case "query":
		m.focus = 0
		return m.input.Focus()
	case "roots":
		return m.showRoots()
	case "filters":
		return m.showFilters()
	case "preview":
		m.focus = 2
		m.input.Blur()
		return nil
	}
	if strings.HasPrefix(h, "row:") {
		id := strings.TrimPrefix(h, "row:")
		for n, i := range m.rows {
			if i.ID == id {
				m.focus = 1
				m.input.Blur()
				return tea.Batch(m.selectRow(n), m.accept())
			}
		}
	}
	if strings.HasPrefix(h, "source:") {
		s := strings.TrimPrefix(h, "source:")
		var ss []string
		for _, v := range m.query.Sources {
			if v != s {
				ss = append(ss, v)
			}
		}
		if !contains(m.query.Sources, s) {
			ss = append(ss, s)
		}
		if len(ss) == 0 {
			return nil
		}
		m.query.Sources = ss
		return m.changedScope()
	}
	if strings.HasPrefix(h, "sort:") {
		s := strings.TrimPrefix(h, "sort:")
		if m.sort == s {
			m.desc = !m.desc
		} else {
			m.sort = s
			m.desc = false
		}
		m.rebuild()
	}
	return nil
}
func (m *Model) mouseWheel(v tea.Mouse) tea.Cmd {
	if !m.mouse {
		return nil
	}
	delta := 3
	if v.Button == tea.MouseWheelUp {
		delta = -3
	}
	if m.overlay != nil {
		if m.isPopup() && !m.popupRect().contains(v.X, v.Y) {
			return nil
		}
		m.overlay.selected = max(0, min(len(m.overlay.visible())-1, m.overlay.selected+delta))
		return nil
	}
	g := m.layout()
	if (g.split && v.X > g.listWidth) || (!g.split && m.focus == 2) {
		if delta > 0 {
			m.viewport.ScrollDown(delta)
		} else {
			m.viewport.ScrollUp(-delta)
		}
		return nil
	}
	return m.selectRow(m.selected + delta)
}
