package preview

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/daviddwlee84/lazyfind/internal/actions"
	"github.com/daviddwlee84/lazyfind/internal/config"
	"github.com/daviddwlee84/lazyfind/internal/domain"
)

func setup(t *testing.T) (*Service, domain.Item) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("first content\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Paths: config.Paths{Cache: filepath.Join(dir, "cache")}, Cache: config.Cache{MaxBytes: 1 << 20}}
	return New(cfg), domain.Item{ID: domain.ItemID(domain.Target{}, path), Path: path, Name: "test.txt", Kind: "file"}
}
func TestCacheRechecksMetadataAndInvalidation(t *testing.T) {
	s, item := setup(t)
	resolved := actions.Resolved{MIME: "text/plain"}
	first, err := s.LoadResolved(context.Background(), item, "", resolved)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first, "first content") {
		t.Fatalf("preview: %s", first)
	}
	if err = os.WriteFile(item.Path, []byte("second content is different\n"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := s.LoadResolved(context.Background(), item, "", resolved)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(second, "second content") {
		t.Fatal("stale preview returned")
	}
	info, err := os.Stat(item.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(item.Path, []byte("third! content is different\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(item.Path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	s.Invalidate(item)
	third, err := s.LoadResolved(context.Background(), item, "", resolved)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(third, "third!") {
		t.Fatal("invalidation did not refresh")
	}
	if err = os.Remove(item.Path); err != nil {
		t.Fatal(err)
	}
	if _, err = s.LoadResolved(context.Background(), item, "", resolved); err == nil {
		t.Fatal("deleted file returned cached preview")
	}
}
func TestBoundedOutputAndTerminalControls(t *testing.T) {
	s, item := setup(t)
	data := "\x1b]52;evil\a" + strings.Repeat("a", MaxBytes*2)
	if err := os.WriteFile(item.Path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	text, err := s.LoadResolved(context.Background(), item, "", actions.Resolved{MIME: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(text, "\x1b\a") {
		t.Fatal("preview leaked terminal control")
	}
	if !strings.Contains(text, "truncated") || len(text) > MaxBytes+100 {
		t.Fatalf("preview unbounded: %d", len(text))
	}
}
func TestPreviewCommandCancelledAfterOutputLimit(t *testing.T) {
	s, item := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// A real long-running producer verifies that hitting a display limit ends the
	// child rather than waiting for it to finish or accumulating all its output.
	text, err := s.command(ctx, item.Target, []string{"sh", "-c", "while :; do printf '1234567890123456789012345678901234567890\\n'; done"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "truncated") {
		t.Fatal("truncation not marked")
	}
	if ctx.Err() != nil {
		t.Fatal("output limit waited for timeout")
	}
}
func TestDocumentSnippetsRemainExtractedLocations(t *testing.T) {
	s, item := setup(t)
	item.Matches = []domain.Match{{Source: "document", Line: 42, Text: "Extracted finding", Extracted: true}}
	text, err := s.LoadResolved(context.Background(), item, "", actions.Resolved{MIME: "application/pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "extracted line 42") || !strings.Contains(text, "Extracted finding") {
		t.Fatalf("lost document locator: %s", text)
	}
	if strings.Contains(text, "first content") {
		t.Fatal("binary document was opened as text")
	}
}
func TestCustomPreviewRequiresPreviewMode(t *testing.T) {
	s, item := setup(t)
	resolved := actions.Resolved{Preview: "editor", Actions: []actions.ResolvedAction{{Action: config.Action{ID: "editor", Mode: "suspend", Argv: []string{"vi", item.Path}}, Available: true}}}
	if _, err := s.LoadResolved(context.Background(), item, "", resolved); err == nil {
		t.Fatal("interactive editor accepted as preview")
	}
}

func TestUnicodePreviewTruncation(t *testing.T) {
	s, item := setup(t)
	if err := os.WriteFile(item.Path, []byte(strings.Repeat("搜尋檔案🦊", MaxBytes)), 0600); err != nil {
		t.Fatal(err)
	}
	text, err := s.LoadResolved(context.Background(), item, "", actions.Resolved{MIME: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(text) || !strings.Contains(text, "truncated") {
		t.Fatal("invalid Unicode after truncation")
	}
}

func TestSymlinkPreviewDoesNotCacheTargetUnderLinkMetadata(t *testing.T) {
	s, item := setup(t)
	link := item.Path + ".link"
	if err := os.Symlink(item.Path, link); err != nil {
		t.Fatal(err)
	}
	linked := domain.Item{ID: domain.ItemID(domain.Target{}, link), Path: link, Kind: "symlink"}
	resolved := actions.Resolved{MIME: "text/plain"}
	if _, err := s.LoadResolved(context.Background(), linked, "", resolved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(item.Path, []byte("changed target content\n"), 0600); err != nil {
		t.Fatal(err)
	}
	text, err := s.LoadResolved(context.Background(), linked, "", resolved)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "changed target content") {
		t.Fatal("symlink's unchanged mtime masked target changes")
	}
}
