package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazyfind/internal/domain"
	"github.com/daviddwlee84/lazyfind/internal/preview"
)

func TestYellowHighlightRemainsVisibleOnSelectedRows(t *testing.T) {
	m := matchModel(t)
	m.cfg.UI.Color = "always"
	m.cfg.UI.HighlightMatches = true
	raw := "before hit after"
	for _, selected := range []bool{false, true} {
		got := m.highlight(raw, []domain.Span{{Start: 7, End: 10}}, selected)
		if ansi.Strip(got) != raw {
			t.Fatalf("highlight changed text: %q", got)
		}
		// Lip Gloss combines foreground black and ANSI yellow background.
		if !strings.Contains(got, "43mhit") && !strings.Contains(got, "43;30mhit") {
			t.Fatalf("yellow highlight missing (selected=%v): %q", selected, got)
		}
		if selected && !strings.Contains(got, "\x1b[7m") {
			t.Fatalf("selection disappeared: %q", got)
		}
	}
}
func TestHighlightRespectsSettingsAndNOColor(t *testing.T) {
	m := matchModel(t)
	span := []domain.Span{{Start: 0, End: 3}}
	m.cfg.UI.Color = "never"
	m.cfg.UI.HighlightMatches = true
	if got := m.highlight("hit", span, true); got != "hit" {
		t.Fatalf("color=never: %q", got)
	}
	m.cfg.UI.Color = "auto"
	t.Setenv("NO_COLOR", "1")
	if got := m.highlight("hit", span, true); got != "hit" {
		t.Fatalf("NO_COLOR: %q", got)
	}
	m.cfg.UI.Color = "always"
	m.cfg.UI.HighlightMatches = false
	if got := m.highlight("hit", span, false); got != "hit" {
		t.Fatalf("highlight=false: %q", got)
	}
}
func TestHighlightNeverSplitsGraphemes(t *testing.T) {
	m := matchModel(t)
	m.cfg.UI.Color = "always"
	m.cfg.UI.HighlightMatches = true
	text := "ae\u0301👨‍👩‍👧‍👦z"
	got := m.highlight(text, []domain.Span{{Start: 1, End: 2}, {Start: 4, End: 8}}, false)
	if ansi.Strip(got) != text || !strings.Contains(got, "e\u0301👨‍👩‍👧‍👦") {
		t.Fatalf("split combining or emoji cluster: %q", got)
	}
}
func TestPathHighlightMapsRawRootAndControls(t *testing.T) {
	m := matchModel(t)
	m.cfg.UI.Color = "always"
	m.cfg.UI.HighlightMatches = true
	m.query.Text = "hit"
	m.query.FullPath = false
	raw := "/root/中文/\thit\x1b.txt"
	item := domain.Item{Path: raw, Sources: []string{"names"}}
	got := m.pathText(item, "中文/\thit\x1b.txt", false)
	if strings.ContainsAny(ansi.Strip(got), "\t\x1b") || !strings.Contains(got, "43mhit") {
		t.Fatalf("offset after sanitization: %q", got)
	}
	m.query.Text = "HIT"
	got = m.pathText(item, "中文/\thit\x1b.txt", false)
	if strings.Contains(got, "43") {
		t.Fatalf("smart case ignored: %q", got)
	}
}
func TestLiteralRegexAndFuzzySpans(t *testing.T) {
	if got := textSpans("a.b aXb", "a.b", false); !reflect.DeepEqual(got, []domain.Span{{Start: 0, End: 3}}) {
		t.Fatalf("literal %v", got)
	}
	if got := textSpans("a.b aXb", "a.b", true); !reflect.DeepEqual(got, []domain.Span{{Start: 0, End: 3}, {Start: 4, End: 7}}) {
		t.Fatalf("regex %v", got)
	}
	if got := textSpans("中文 HIT hit", "hit", false); len(got) != 2 {
		t.Fatalf("smartcase %v", got)
	}
	if got := fuzzySpans("中文hit", "中hT"); !reflect.DeepEqual(got, []domain.Span{{Start: 0, End: 3}, {Start: 6, End: 7}, {Start: 8, End: 9}}) {
		t.Fatalf("fuzzy %v", got)
	}
	if got := fuzzySpans("partial", "px"); got != nil {
		t.Fatalf("incomplete fuzzy match %v", got)
	}
}
func TestPreviewLineNumbersToggleWithoutLoading(t *testing.T) {
	m := matchModel(t)
	m.cfg.UI.Color = "never"
	m.cfg.UI.PreviewLineNumbers = true
	doc := &preview.Document{Metadata: "fixture metadata", Lines: []preview.Line{{Number: 42, Text: "native hit", Spans: []domain.Span{{Start: 7, End: 10}}}, {Number: 43, Text: "next source line"}}}
	m.previewDoc = doc
	m.previewBody = ""
	m.previewSvc = nil
	m.actionSvc = nil
	initialGeneration := m.previewGen
	canceled := false
	m.previewCancel = func() { canceled = true }
	m.refreshPreview()
	if !strings.Contains(m.viewport.View(), "42 │ native hit") {
		t.Fatalf("source line numbers absent: %s", m.viewport.View())
	}
	if cmd := m.command("preview_lines"); cmd != nil {
		t.Fatal("line-number toggle scheduled I/O")
	}
	if strings.Contains(m.viewport.View(), "42 │") || !strings.Contains(m.viewport.View(), "native hit") {
		t.Fatalf("toggle did not rerender: %s", m.viewport.View())
	}
	if canceled || m.previewGen != initialGeneration || m.previewDoc != doc {
		t.Fatal("pure display preference changed preview ownership")
	}
	if cmd := m.command("preview_lines"); cmd != nil || !strings.Contains(m.viewport.View(), "42 │ native hit") {
		t.Fatal("toggle did not restore source numbers")
	}
}
func TestPreviewCustomAndExtractedLocationsNeverBecomeNativeNumbers(t *testing.T) {
	m := matchModel(t)
	m.cfg.UI.Color = "never"
	m.cfg.UI.PreviewLineNumbers = true
	m.previewDoc = &preview.Document{Metadata: "snapshot", Opaque: "custom output line", Snippets: []domain.Match{{Source: "documents", Line: 23, Text: "extracted content", Extracted: true}}}
	m.previewBody = ""
	m.refreshPreview()
	got := m.viewport.View()
	if !strings.Contains(got, "documents extracted line 23: extracted content") || !strings.Contains(got, "custom output line") || strings.Contains(got, "│ custom output") {
		t.Fatalf("false source locations: %s", got)
	}
}
