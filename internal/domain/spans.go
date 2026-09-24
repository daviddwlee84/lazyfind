package domain

import (
	"sort"
	"strings"
	"unicode"

	"github.com/rivo/uniseg"
)

// Span is a half-open UTF-8 byte interval, never a terminal-cell interval.
type Span struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// SanitizeSpans makes a single raw line safe for display while retaining its
// match locations. Tabs use four-cell stops; controls and invalid UTF-8 become
// replacement characters. A match touching a grapheme highlights that complete
// grapheme. limit is a byte budget including an ellipsis; negative means unlimited.
func SanitizeSpans(raw string, spans []Span, limit int) (string, []Span) {
	if limit == 0 {
		return "", nil
	}
	var b strings.Builder
	var mapped []Span
	column, truncated := 0, false
	g := uniseg.NewGraphemes(raw)
	for g.Next() {
		start, end := g.Positions()
		var cluster strings.Builder
		for _, r := range g.Str() {
			switch {
			case r == '\t':
				cluster.WriteString(strings.Repeat(" ", 4-column%4))
			case unicode.IsControl(r):
				cluster.WriteRune('�')
			default:
				cluster.WriteRune(r)
			}
		}
		text := cluster.String()
		if limit >= 0 && b.Len()+len(text) > limit {
			truncated = true
			break
		}
		outStart := b.Len()
		b.WriteString(text)
		column += uniseg.StringWidth(text)
		for _, span := range spans {
			if span.End > span.Start && span.Start < end && span.End > start {
				mapped = append(mapped, Span{outStart, b.Len()})
				break
			}
		}
	}
	text := b.String()
	if truncated {
		return clip(text, mapped, limit, true)
	}
	return text, mergeSpans(mapped)
}

// ClipSpans truncates already-sanitized text at grapheme boundaries, retaining
// only visible match spans. It never modifies the caller's slices. Unlike
// SanitizeSpans it does not expand tabs or otherwise transform stored text.
func ClipSpans(text string, spans []Span, limit int) (string, []Span) {
	return clip(text, spans, limit, limit >= 0 && len(text) > limit)
}

func clip(text string, spans []Span, limit int, ellipsis bool) (string, []Span) {
	if limit == 0 {
		return "", nil
	}
	mark := ""
	budget := limit
	if ellipsis {
		mark = "…"
		if limit < len(mark) {
			mark = strings.Repeat(".", max(0, limit))
		}
		budget = limit - len(mark)
	}
	end := 0
	var mapped []Span
	g := uniseg.NewGraphemes(text)
	for g.Next() {
		start, next := g.Positions()
		if budget >= 0 && next > budget {
			break
		}
		end = next
		for _, span := range spans {
			if span.End > span.Start && span.Start < next && span.End > start {
				mapped = append(mapped, Span{start, next})
				break
			}
		}
	}
	return text[:end] + mark, mergeSpans(mapped)
}

func mergeSpans(spans []Span) []Span {
	if len(spans) == 0 {
		return nil
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].Start < spans[j].Start })
	out := []Span{spans[0]}
	for _, span := range spans[1:] {
		last := &out[len(out)-1]
		if span.Start <= last.End {
			last.End = max(last.End, span.End)
		} else {
			out = append(out, span)
		}
	}
	return out
}
