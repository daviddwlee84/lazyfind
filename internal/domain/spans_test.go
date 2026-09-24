package domain

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeSpansMapsDisplayTransformations(t *testing.T) {
	tests := []struct {
		name, raw, want, needle string
		span                    Span
	}{
		{"tabs", "\t中文\thit", "    中文    hit", "hit", Span{8, 11}},
		{"control", "x\x1b[31mhit", "x�[31mhit", "hit", Span{6, 9}},
		{"invalid bytes", "a\xff\xfeb", "a��b", "b", Span{3, 4}},
		{"combining", "ae\u0301z", "ae\u0301z", "e\u0301", Span{1, 2}},
		{"emoji grapheme", "x👨‍👩‍👧‍👦y", "x👨‍👩‍👧‍👦y", "👨‍👩‍👧‍👦", Span{1, 5}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, spans := SanitizeSpans(tt.raw, []Span{tt.span}, -1)
			if text != tt.want {
				t.Fatalf("text %q, want %q", text, tt.want)
			}
			if len(spans) != 1 || text[spans[0].Start:spans[0].End] != tt.needle {
				t.Fatalf("mapped %v in %q", spans, text)
			}
		})
	}
}
func TestSpansTruncateAtGraphemeBoundaryWithoutHighlightingEllipsis(t *testing.T) {
	text, spans := SanitizeSpans("a界bcdef", []Span{{0, 9}}, 6)
	if text != "a…" || !reflect.DeepEqual(spans, []Span{{0, 1}}) {
		t.Fatalf("%q %v", text, spans)
	}
	original := []Span{{0, 20}}
	text, spans = ClipSpans("e\u0301👨‍👩‍👧‍👦z", original, 8)
	if text != "e\u0301…" || !reflect.DeepEqual(spans, []Span{{0, 3}}) {
		t.Fatalf("%q %v", text, spans)
	}
	if original[0].End != 20 {
		t.Fatal("caller spans mutated")
	}
	text, spans = ClipSpans("abc", []Span{{0, 3}}, 0)
	if text != "" || len(spans) != 0 {
		t.Fatal("zero byte limit ignored")
	}
}
func TestSpansMergeAndIgnoreEmptyRanges(t *testing.T) {
	text, spans := SanitizeSpans("abcd", []Span{{2, 4}, {0, 2}, {2, 2}, {3, 1}}, -1)
	if text != "abcd" || !reflect.DeepEqual(spans, []Span{{0, 4}}) {
		t.Fatalf("%q %v", text, spans)
	}
}
func TestSpansBoundExpansionAndMalformedText(t *testing.T) {
	raw := strings.Repeat("\xff\t\x1b界e\u0301", 1000)
	for limit := 0; limit < 40; limit++ {
		text, spans := SanitizeSpans(raw, []Span{{0, len(raw)}}, limit)
		if len(text) > limit || !utf8.ValidString(text) {
			t.Fatalf("limit=%d: %q", limit, text)
		}
		for _, span := range spans {
			if span.Start < 0 || span.End > len(text) || span.End <= span.Start {
				t.Fatalf("bad spans %v", spans)
			}
		}
	}
}
