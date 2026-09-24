package tui

import (
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazyfind/internal/config"
	"github.com/daviddwlee84/lazyfind/internal/domain"
)

func normalizedKey(key string) string {
	return config.CanonicalKey(key)
}
func matchesKey(key, binding string) bool { return normalizedKey(key) == normalizedKey(binding) }

// Bounds are rune offsets, matching the textinput cursor, not byte offsets.
func completionToken(raw string, cursor int) (start, end int, token string, ok bool) {
	rs := []rune(raw)
	cursor = max(0, min(cursor, len(rs)))
	start = cursor
	end = cursor
	quote := rune(0)
	escaped := false
	for i, r := range rs[:cursor] {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
		}
		if unicode.IsSpace(r) {
			start = i + 1
		}
	}
	if quote != 0 {
		return 0, 0, "", false
	}
	if start == cursor {
		for start > 0 && !unicode.IsSpace(rs[start-1]) {
			start--
		}
	}
	for end < len(rs) && !unicode.IsSpace(rs[end]) {
		end++
	}
	token = string(rs[start:cursor])
	if strings.ContainsAny(string(rs[start:end]), "\"'\\") {
		return 0, 0, "", false
	}
	return start, end, token, true
}
func (m *Model) completionChoices(explicit bool) ([]choice, int, int) {
	start, end, token, ok := completionToken(m.input.Value(), m.input.Position())
	if !ok {
		if explicit {
			return appendConditions(m.input.Value())
		}
		return nil, 0, 0
	}
	key, value, colon := strings.Cut(token, ":")
	var choices []choice
	if !colon {
		if !explicit {
			return nil, start, end
		}
		for _, q := range domain.Qualifiers() {
			if strings.HasPrefix(q.Name, key) {
				choices = append(choices, choice{label: q.Name + ":  — " + q.Description + "  e.g. " + q.Example, value: q.Name + ":"})
			}
		}
		if len(choices) == 0 && explicit {
			return appendConditions(m.input.Value())
		}
		return choices, start, end
	}
	known := false
	for _, q := range domain.Qualifiers() {
		if q.Name != key {
			continue
		}
		known = true
		values := append([]string(nil), q.Values...)
		if key == "after" || key == "before" {
			values = []string{time.Now().Format("2006-01-02")}
		}
		prefix := ""
		fragment := value
		if key == "ext" || key == "type" {
			if n := strings.LastIndex(value, ","); n >= 0 {
				prefix = value[:n+1]
				fragment = value[n+1:]
			}
		}
		if key == "ext" {
			seen := map[string]bool{}
			for _, v := range values {
				seen[v] = true
			}
			for _, i := range m.items {
				ext := strings.TrimPrefix(filepath.Ext(i.RawPath()), ".")
				if ext != "" && !seen[ext] && !strings.ContainsAny(ext, " \t\r\n,:\"'") {
					seen[ext] = true
					values = append(values, ext)
				}
			}
			sort.Strings(values)
		}
		for _, v := range values {
			if strings.HasPrefix(v, fragment) && !contains(strings.Split(strings.TrimSuffix(prefix, ","), ","), v) {
				choices = append(choices, choice{label: key + ":" + prefix + v + "  — " + q.Description, value: key + ":" + prefix + v})
			}
		}
		break
	}
	if !known && explicit {
		return appendConditions(m.input.Value())
	}
	return choices, start, end
}
func appendConditions(raw string) ([]choice, int, int) {
	prefix := ""
	rs := []rune(raw)
	if len(rs) > 0 && !unicode.IsSpace(rs[len(rs)-1]) {
		prefix = " "
	}
	var cs []choice
	for _, q := range domain.Qualifiers() {
		cs = append(cs, choice{label: q.Name + ":  — " + q.Description + "  e.g. " + q.Example, value: prefix + q.Name + ":"})
	}
	return cs, len(rs), len(rs)
}
func (m *Model) showCompletion(explicit bool) tea.Cmd {
	cs, start, end := m.completionChoices(explicit)
	if len(cs) == 0 && !explicit {
		if m.overlay != nil && m.overlay.kind == "completion" {
			m.overlay = nil
		}
		return nil
	}
	m.overlay = &overlay{kind: "completion", title: "Conditions · Enter/Tab insert · Esc close", choices: cs, replaceStart: start, replaceEnd: end}
	m.pressed = ""
	return nil
}
func (m *Model) acceptCompletion() tea.Cmd {
	o := m.overlay
	cs := o.visible()
	if o.selected < 0 || o.selected >= len(cs) {
		return nil
	}
	rs := []rune(m.input.Value())
	if o.replaceStart < 0 || o.replaceEnd > len(rs) {
		m.overlay = nil
		return nil
	}
	value := cs[o.selected].value
	if !strings.HasSuffix(value, ":") && o.replaceEnd == len(rs) {
		value += " "
	}
	old := m.input.Value()
	next := string(rs[:o.replaceStart]) + value + string(rs[o.replaceEnd:])
	pos := o.replaceStart + len([]rune(value))
	m.overlay = nil
	m.input.SetValue(next)
	m.input.SetCursor(pos)
	cmd := m.queryEdited(old, nil)
	if strings.HasSuffix(value, ":") {
		m.showCompletion(true)
	}
	return cmd
}
func (m *Model) completionKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		m.overlay = nil
		m.pressed = ""
		return nil
	case "up":
		m.overlay.selected = max(0, m.overlay.selected-1)
		return nil
	case "down":
		m.overlay.selected = min(max(0, len(m.overlay.choices)-1), m.overlay.selected+1)
		return nil
	case "enter", "tab":
		return m.acceptCompletion()
	}
	old := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	return m.queryEdited(old, cmd)
}
func (m *Model) showHelp() tea.Cmd {
	o := &overlay{kind: "help", title: "Help · type to find · arrows/wheel scroll · Esc close", input: field("Find help", "")}
	add := func(s string) { o.choices = append(o.choices, choice{label: s, value: s}) }
	for _, s := range []string{"FLOW  1. r: choose roots/host; type your keyword", "FLOW  2. f: filters, or Ctrl+Space: query conditions", "FLOW  3. Ctrl+F: filter loaded results — no new search", "FLOW  4. n/N: inspect hits; Enter or : chooses a tool", "FLOW  5. y: copy path/location; H: saved searches", "SEARCH  Empty-query scanning: configurable auto_search_empty", "LIMIT  max_results limits matches, not visited files", "USAGE  u: selected/visible directories; recursive, may be slow"} {
		add(s)
	}
	for _, id := range []string{"search", "insert_search", "complete_query", "result_filter", "roots", "sources", "filters", "sort", "history", "actions", "copy_menu", "directory_usage", "next_match", "previous_match", "preview", "preview_lines", "refresh", "mouse", "quit"} {
		add(m.cfg.Keymap[id] + "  " + strings.ReplaceAll(id, "_", " "))
	}
	for _, q := range domain.Qualifiers() {
		add(q.Name + ":  " + q.Description + "  e.g. " + q.Example)
	}
	add("HISTORY  Ctrl+D / Delete button deletes only the selected snapshot")
	add("REMOTE  Enter submits. Remote cancellation is best effort.")
	m.overlay = o
	return o.input.Focus()
}
