package search

import (
	"context"
	"encoding/binary"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestRealRGMatchRawAndDisplaySpans(t *testing.T) {
	cfg := realTools(t)
	root := t.TempDir()
	raw := "\t中文\t\x1b[31mhit\x1b[0m and hit\n"
	write(t, filepath.Join(root, "plain.txt"), raw)
	write(t, filepath.Join(root, "bom.txt"), "\xef\xbb\xbf"+raw)
	write(t, filepath.Join(root, "invalid.txt"), "\xffhit\n")
	units := utf16.Encode([]rune(raw))
	encoded := []byte{0xff, 0xfe}
	for _, u := range units {
		encoded = binary.LittleEndian.AppendUint16(encoded, u)
	}
	write(t, filepath.Join(root, "utf16.txt"), string(encoded))
	run := New(cfg).Execute(context.Background(), parsed(t, root, "hit"), nil)
	if run.Status != "complete" {
		t.Fatalf("%+v", run)
	}
	for _, name := range []string{"plain.txt", "bom.txt", "utf16.txt"} {
		item := findItem(t, run, name)
		if len(item.Matches) != 2 {
			t.Fatalf("%s: %+v", name, item.Matches)
		}
		for i, m := range item.Matches {
			start := strings.Index(raw, "hit")
			if i == 1 {
				start = strings.LastIndex(raw, "hit")
			}
			if m.RawSpan == nil || m.RawSpan.Start != start || m.RawSpan.End != start+3 || m.Column != start+1 {
				t.Fatalf("%s raw: %+v", name, m)
			}
			if len(m.Spans) != 1 || m.Text[m.Spans[0].Start:m.Spans[0].End] != "hit" {
				t.Fatalf("%s display: %+v", name, m)
			}
			if strings.ContainsAny(m.Text, "\t\x1b") {
				t.Fatalf("unsafe display: %q", m.Text)
			}
		}
	}
	item := findItem(t, run, "invalid.txt")
	m := item.Matches[0]
	if m.RawSpan.Start != 1 || m.Spans[0].Start != 3 || m.Text[m.Spans[0].Start:m.Spans[0].End] != "hit" {
		t.Fatalf("invalid UTF8 mapping: %+v", m)
	}
}
func TestLongMatchKeepsRawSpanAfterSnippetClipping(t *testing.T) {
	cfg := realTools(t)
	root := t.TempDir()
	write(t, filepath.Join(root, "long.txt"), strings.Repeat("a", 4096)+"hit\n")
	run := New(cfg).Execute(context.Background(), parsed(t, root, "hit"), nil)
	m := findItem(t, run, "long.txt").Matches[0]
	if m.RawSpan == nil || m.RawSpan.Start != 4096 || m.RawSpan.End != 4099 || len(m.Spans) != 0 || len(m.Text) > 2048 {
		t.Fatalf("%+v", m)
	}
}
