package preview

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/daviddwlee84/lazyfind/internal/actions"
	"github.com/daviddwlee84/lazyfind/internal/config"
	"github.com/daviddwlee84/lazyfind/internal/domain"
)

func searchMetadata(t *testing.T, s *Service, item domain.Item) domain.Item {
	t.Helper()
	current, err := s.search.Inspect(context.Background(), item.Target, item.RawPath())
	if err != nil {
		t.Fatal(err)
	}
	item.Size = current.Size
	item.Modified = current.Modified
	return item
}
func TestDocumentNativeSpansAndStaleMetadata(t *testing.T) {
	s, item := setup(t)
	raw := "\t中文 \x1b[31mhit\x1b[0m\nsecond hit\n"
	if err := os.WriteFile(item.Path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	item = searchMetadata(t, s, item)
	start := strings.Index(raw, "hit")
	item.Matches = []domain.Match{{Source: "text", Line: 1, RawSpan: &domain.Span{Start: start, End: start + 3}, Text: "old snippet"}, {Source: "text", Line: 2, RawSpan: &domain.Span{Start: 7, End: 10}}}
	resolved := actions.Resolved{MIME: "text/plain"}
	doc, err := s.LoadDocument(context.Background(), item, "hit", resolved)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Changed || len(doc.Lines) != 2 || doc.Lines[0].Number != 1 {
		t.Fatalf("%+v", doc)
	}
	for _, line := range doc.Lines {
		if len(line.Spans) != 1 || line.Text[line.Spans[0].Start:line.Spans[0].End] != "hit" {
			t.Fatalf("%+v", line)
		}
	}
	// Rendering line-number preferences is pure and preserves the same model.
	if !strings.Contains(doc.Text(true), "1 │") || strings.Contains(doc.Text(false), "1 │") {
		t.Fatal("line number rendering")
	}
	if err := os.WriteFile(item.Path, []byte("newer hit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := s.LoadDocument(context.Background(), item, "hit", resolved)
	if err != nil {
		t.Fatal(err)
	}
	if !changed.Changed || len(changed.Lines) != 1 || len(changed.Lines[0].Spans) != 0 || changed.Snippets[0].Text != "old snippet" {
		t.Fatalf("%+v", changed)
	}
	// Old snapshots lack reliable native spans even if their current metadata fits.
	item = searchMetadata(t, s, item)
	item.Matches[0].RawSpan = nil
	doc, err = s.LoadDocument(context.Background(), item, "hit", resolved)
	if err != nil || len(doc.Lines[0].Spans) != 0 {
		t.Fatalf("legacy mapping: %+v %v", doc, err)
	}
}
func TestDocumentDecodesBOMAndUTF16(t *testing.T) {
	for _, encoding := range []string{"utf8", "utf16le", "utf16be"} {
		t.Run(encoding, func(t *testing.T) {
			s, item := setup(t)
			text := "一\thit\n二 hit\n"
			var raw []byte
			if encoding == "utf8" {
				raw = append([]byte{0xef, 0xbb, 0xbf}, []byte(text)...)
			} else {
				var order binary.AppendByteOrder = binary.LittleEndian
				raw = []byte{0xff, 0xfe}
				if encoding == "utf16be" {
					order = binary.BigEndian
					raw = []byte{0xfe, 0xff}
				}
				for _, u := range utf16.Encode([]rune(text)) {
					raw = order.AppendUint16(raw, u)
				}
			}
			if err := os.WriteFile(item.Path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			item = searchMetadata(t, s, item)
			item.Matches = []domain.Match{{Line: 1, RawSpan: &domain.Span{Start: 4, End: 7}}, {Line: 2, RawSpan: &domain.Span{Start: 4, End: 7}}}
			doc, err := s.LoadDocument(context.Background(), item, "hit", actions.Resolved{MIME: "text/plain"})
			if err != nil {
				t.Fatal(err)
			}
			if len(doc.Lines) != 2 {
				t.Fatalf("%+v", doc)
			}
			for _, line := range doc.Lines {
				if len(line.Spans) != 1 || line.Text[line.Spans[0].Start:line.Spans[0].End] != "hit" {
					t.Fatalf("%+v", line)
				}
			}
		})
	}
}
func TestDocumentUnsupportedAndMalformedEncodingOpaque(t *testing.T) {
	for _, raw := range [][]byte{{0xff, 0xfe, 0, 0, 'a', 0, 0, 0}, {0xff, 0xfe, 0, 0xd8}, {'a', 0, 'b'}} {
		s, item := setup(t)
		if err := os.WriteFile(item.Path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		doc, err := s.LoadDocument(context.Background(), item, "", actions.Resolved{MIME: "text/plain"})
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.Lines) != 0 || doc.Opaque == "" {
			t.Fatalf("unsafe native mapping: %+v", doc)
		}
	}
}
func TestCustomPreviewAndOverriddenBatRemainOpaque(t *testing.T) {
	for _, id := range []string{"custom", "bat"} {
		t.Run(id, func(t *testing.T) {
			s, item := setup(t)
			s.custom[id] = true
			resolved := actions.Resolved{MIME: "text/plain", Preview: id, Actions: []actions.ResolvedAction{{Action: config.Action{ID: id, Mode: "preview", Argv: []string{"printf", "custom output\\n"}}, Available: true}}}
			doc, err := s.LoadDocument(context.Background(), item, "", resolved)
			if err != nil {
				t.Fatal(err)
			}
			if len(doc.Lines) != 0 || doc.Opaque != "custom output\n" {
				t.Fatalf("%+v", doc)
			}
		})
	}
}
func TestDocumentCacheStoresVersionedStructure(t *testing.T) {
	s, item := setup(t)
	item = searchMetadata(t, s, item)
	item.Matches = []domain.Match{{Line: 1, RawSpan: &domain.Span{Start: 0, End: 5}}}
	resolved := actions.Resolved{MIME: "text/plain"}
	doc, err := s.LoadDocument(context.Background(), item, "first", resolved)
	if err != nil {
		t.Fatal(err)
	}
	cached, err := s.LoadDocument(context.Background(), item, "first", resolved)
	if err != nil || !reflect.DeepEqual(doc, cached) {
		t.Fatalf("cache mismatch %v", err)
	}
	files, err := filepath.Glob(filepath.Join(filepath.Dir(item.Path), "cache", "previews", "*.preview"))
	if err != nil || len(files) != 1 {
		t.Fatalf("cache %v %v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var entry struct {
		Version  int
		Document Document
	}
	if err = json.Unmarshal(data, &entry); err != nil || entry.Version != 2 || len(entry.Document.Lines[0].Spans) != 1 {
		t.Fatalf("bad cached document %s %v", data, err)
	}
}
func TestRealBatPlainWindowPreservesSourceLocations(t *testing.T) {
	if _, err := exec.LookPath("bat"); err != nil {
		t.Skip("bat unavailable")
	}
	for _, encoding := range []string{"utf8", "utf16"} {
		t.Run(encoding, func(t *testing.T) {
			s, item := setup(t)
			var b strings.Builder
			for i := 1; i <= 180; i++ {
				if i == 100 {
					b.WriteString("\t中文 \x1b[31mhit\x1b[0m\n")
				} else {
					b.WriteString("normal\n")
				}
			}
			raw := []byte(b.String())
			if encoding == "utf16" {
				raw = []byte{0xff, 0xfe}
				for _, u := range utf16.Encode([]rune(b.String())) {
					raw = binary.LittleEndian.AppendUint16(raw, u)
				}
			}
			if err := os.WriteFile(item.Path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			item = searchMetadata(t, s, item)
			item.Matches = []domain.Match{{Source: "text", Line: 100, RawSpan: &domain.Span{Start: 13, End: 16}}}
			// Use the production action resolver so this also validates the actual flags.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			resolved, err := s.actions.Resolve(ctx, item, "hit")
			if err != nil {
				t.Fatal(err)
			}
			if resolved.Preview != "bat" {
				t.Fatalf("bat unavailable: %+v", resolved)
			}
			doc, err := s.LoadDocument(ctx, item, "hit", resolved)
			if err != nil {
				t.Fatal(err)
			}
			if len(doc.Lines) == 0 || doc.Lines[0].Number != 60 {
				t.Fatalf("lost range: %+v", doc)
			}
			for _, line := range doc.Lines {
				if line.Number == 100 {
					if len(line.Spans) != 1 || line.Text[line.Spans[0].Start:line.Spans[0].End] != "hit" {
						t.Fatalf("%+v", line)
					}
					return
				}
			}
			t.Fatal("selected native line missing")
		})
	}
}
