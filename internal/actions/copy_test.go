package actions

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/daviddwlee84/lazyfind/internal/domain"
)

func copyFormat(t *testing.T, item domain.Item, roots []string, selected int, id string) CopyFormat {
	t.Helper()
	for _, format := range Formats(item, roots, selected) {
		if format.ID == id {
			return format
		}
	}
	t.Fatalf("missing format %q", id)
	return CopyFormat{}
}

func TestCopyUsesDeepestContainingRootAndSelectedNativeMatch(t *testing.T) {
	item := domain.Item{Path: "/offline/project/docs/guide.md", Matches: []domain.Match{{Source: "text", Line: 12}, {Source: "text", Line: 93}}}
	roots := []string{"/offline/project", "/offline/project/docs/", "/offline/project/docs/guide", "relative"}
	for id, want := range map[string]string{
		"absolute":           item.Path,
		"relative":           "guide.md",
		"absolute_line":      item.Path + ":93",
		"relative_line":      "guide.md:93",
		"absolute_reference": "@" + item.Path + ":93",
		"relative_reference": "@guide.md:93",
	} {
		got, err := BuildCopy(item, roots, 1, id)
		if err != nil || got != want {
			t.Errorf("%s: %q, %v; want %q", id, got, err, want)
		}
	}
	format := copyFormat(t, item, roots, 1, "relative")
	if format.Base != "/offline/project/docs" || !strings.Contains(format.Label, format.Base) {
		t.Fatalf("relative base not visible: %+v", format)
	}
	item.Path = "/offline/project/docs"
	if got, err := BuildCopy(item, roots, -1, "relative"); err != nil || got != "." {
		t.Fatalf("root itself: %q, %v", got, err)
	}
}

func TestCopyRootBoundaryAndNoCwdFallback(t *testing.T) {
	for _, item := range []domain.Item{{Path: "/project-other/file"}, {Path: "relative/file"}} {
		format := copyFormat(t, item, []string{"/project", "relative"}, -1, "relative")
		if format.Available || format.Reason == "" || format.Base != "" {
			t.Fatalf("bad relative availability: %+v", format)
		}
		if _, err := BuildCopy(item, []string{"/project"}, -1, "relative"); err == nil {
			t.Fatal("unavailable relative format accepted")
		}
	}
	item := domain.Item{Path: "/project-other/file"}
	if got, err := BuildCopy(item, []string{"/"}, -1, "relative"); err != nil || got != "project-other/file" {
		t.Fatalf("system-root relative: %q %v", got, err)
	}
	if _, err := BuildCopy(item, nil, -1, "unknown"); err == nil {
		t.Fatal("unknown format accepted")
	}
}

func TestCopyRemoteOfflineFormats(t *testing.T) {
	item := domain.Item{Path: "/home/remote/src/a.md", Target: domain.Target{Host: "offline-fixture"}, Matches: []domain.Match{{Line: 7}}}
	for id, want := range map[string]string{
		"remote":             "offline-fixture:/home/remote/src/a.md",
		"remote_line":        "offline-fixture:/home/remote/src/a.md:7",
		"absolute":           "/home/remote/src/a.md",
		"relative":           "src/a.md",
		"relative_reference": "@src/a.md:7",
	} {
		got, err := BuildCopy(item, []string{"/home/remote"}, 0, id)
		if err != nil || got != want {
			t.Errorf("%s = %q, %v; want %q", id, got, err, want)
		}
	}
	format := copyFormat(t, item, nil, 0, "absolute")
	if !strings.Contains(format.Label, item.Target.Host) {
		t.Fatalf("remote absolute label omits host: %+v", format)
	}
	if got := CopyPath(item); got != "offline-fixture:/home/remote/src/a.md" {
		t.Fatalf("legacy CopyPath changed: %q", got)
	}
}

func TestCopyExtractedSelectionNeverBorrowsAnotherMatchLine(t *testing.T) {
	item := domain.Item{Path: "/docs/book.pdf", Target: domain.Target{Host: "offline-fixture"}, Matches: []domain.Match{{Line: 31, Extracted: true}, {Line: 5}}}
	for _, id := range []string{"absolute_line", "relative_line", "remote_line"} {
		format := copyFormat(t, item, []string{"/docs"}, 0, id)
		if format.Available || !strings.Contains(format.Reason, "Extracted") {
			t.Fatalf("extracted match offered native line: %+v", format)
		}
	}
	for _, selected := range []int{0, -1, 10} {
		if got, err := BuildCopy(item, []string{"/docs"}, selected, "relative_reference"); err != nil || got != "@book.pdf" {
			t.Fatalf("file-only reference: %q %v", got, err)
		}
	}
}

func TestCopyRetainsRawFilenameBytes(t *testing.T) {
	raw := "/offline/\xff\n\t' file.md"
	item := domain.Item{Path: "/offline/display-safe.md", PathBytes: base64.StdEncoding.EncodeToString([]byte(raw))}
	if got, err := BuildCopy(item, []string{"/offline"}, -1, "absolute"); err != nil || got != raw {
		t.Fatalf("absolute raw bytes: %q %v", got, err)
	}
	if got, err := BuildCopy(item, []string{"/offline"}, -1, "relative"); err != nil || got != "\xff\n\t' file.md" {
		t.Fatalf("relative raw bytes: %q %v", got, err)
	}
}
