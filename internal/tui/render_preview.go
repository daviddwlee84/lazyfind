package tui

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/daviddwlee84/lazyfind/internal/actions"
	"github.com/daviddwlee84/lazyfind/internal/domain"
)

func (m *Model) highlight(text string, spans []domain.Span, selected bool) string {
	text, spans = domain.ClipSpans(text, spans, -1)
	if !m.cfg.UI.HighlightMatches || !m.colors() {
		if selected {
			return m.selectedStyle(text)
		}
		return text
	}
	var out strings.Builder
	offset := 0
	style := lipgloss.NewStyle().Background(lipgloss.Color("3")).Foreground(lipgloss.Color("0")).Reverse(false)
	plain := func(s string) string {
		if selected {
			return m.selectedStyle(s)
		}
		return s
	}
	for _, s := range spans {
		if s.Start < offset || s.Start < 0 || s.End > len(text) || s.End <= s.Start {
			continue
		}
		out.WriteString(plain(text[offset:s.Start]))
		out.WriteString(style.Render(text[s.Start:s.End]))
		offset = s.End
	}
	out.WriteString(plain(text[offset:]))
	return out.String()
}
func textSpans(text, q string, regex bool) []domain.Span {
	if q == "" {
		return nil
	}
	if !regex {
		q = regexp.QuoteMeta(q)
	}
	if strings.IndexFunc(q, unicode.IsUpper) < 0 {
		q = "(?i)" + q
	}
	re, err := regexp.Compile(q)
	if err != nil {
		return nil
	}
	var spans []domain.Span
	for _, m := range re.FindAllStringIndex(text, -1) {
		if m[1] > m[0] {
			spans = append(spans, domain.Span{Start: m[0], End: m[1]})
		}
	}
	return spans
}
func fuzzySpans(text, q string) []domain.Span {
	if q == "" {
		return nil
	}
	var spans []domain.Span
	offset := 0
	for _, want := range q {
		found := false
		for offset < len(text) {
			r, n := utf8.DecodeRuneInString(text[offset:])
			start := offset
			offset += n
			if unicode.ToLower(r) == unicode.ToLower(want) {
				spans = append(spans, domain.Span{Start: start, End: offset})
				found = true
				break
			}
		}
		if !found {
			return nil
		}
	}
	return spans
}
func (m *Model) pathText(item domain.Item, displayPath string, selected bool) string {
	raw := item.RawPath()
	offset := len(raw) - len(displayPath)
	if offset < 0 {
		offset = 0
	}
	var spans []domain.Span
	if contains(item.Sources, "names") {
		s := raw
		base := 0
		if !m.query.FullPath {
			s = filepath.Base(raw)
			base = len(raw) - len(s)
		}
		for _, span := range textSpans(s, m.query.Text, m.query.Regex) {
			span.Start += base - offset
			span.End += base - offset
			if span.End > 0 && span.Start < len(displayPath) {
				span.Start = max(0, span.Start)
				span.End = min(len(displayPath), span.End)
				spans = append(spans, span)
			}
		}
	}
	for _, s := range fuzzySpans(raw, m.filter.Value()) {
		s.Start -= offset
		s.End -= offset
		if s.End > 0 && s.Start < len(displayPath) {
			s.Start = max(0, s.Start)
			s.End = min(len(displayPath), s.End)
			spans = append(spans, s)
		}
	}
	text, spans := domain.SanitizeSpans(displayPath, spans, -1)
	return m.highlight(text, spans, selected)
}
func (m *Model) refreshPreview() {
	body := m.previewBody
	if d := m.previewDoc; d != nil {
		var b strings.Builder
		b.WriteString(d.Metadata)
		b.WriteString("\n\n")
		if d.Changed {
			b.WriteString("File changed since search; native highlights suppressed.\n")
		}
		if len(d.Lines) > 0 {
			for _, line := range d.Lines {
				if m.cfg.UI.PreviewLineNumbers {
					fmt.Fprintf(&b, "%6d │ ", line.Number)
				}
				b.WriteString(m.highlight(line.Text, line.Spans, false))
				b.WriteByte('\n')
			}
		} else {
			for _, hit := range d.Snippets {
				kind := "line"
				if hit.Extracted {
					kind = "extracted line"
				}
				fmt.Fprintf(&b, "%s %s %d: ", hit.Source, kind, hit.Line)
				b.WriteString(m.highlight(hit.Text, hit.Spans, false))
				b.WriteByte('\n')
			}
		}
		if d.Opaque != "" {
			b.WriteString(d.Opaque)
		}
		if d.Truncated {
			b.WriteString("\n… preview truncated")
		}
		if m.previewBody != "" {
			b.WriteString("\n" + domain.Display(m.previewBody))
		}
		body = b.String()
	}
	if i := m.current(); i != nil {
		if result, ok := m.usageValues[i.ID]; ok {
			body = m.usageDescription(result) + "\n\n" + body
		}
	}
	y := m.viewport.YOffset()
	m.viewport.SetContent(m.decoratePreview(body))
	m.viewport.SetYOffset(y)
}
func (m *Model) idleQuery(q domain.QuerySpec) tea.Cmd {
	save := m.retire()
	m.gen++
	m.edit++
	if m.previewCancel != nil {
		m.previewCancel()
	}
	m.previewGen++
	m.query = q
	m.dirty = false
	m.items = map[string]domain.Item{}
	m.rows = nil
	m.selection = ""
	m.selected = 0
	m.offset = 0
	m.run = domain.Run{}
	m.historical = false
	m.resolved = actions.Resolved{}
	m.previewDoc = nil
	m.previewBody = "Choose roots → type keyword or conditions → Ctrl+F filter results → Enter / : act.\nEnter explicitly lists an empty query."
	m.viewport.SetContent(m.previewBody)
	m.status = "Idle · empty-query automatic search disabled"
	return save
}
