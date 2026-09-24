package search

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daviddwlee84/lazyfind/internal/config"
	"github.com/daviddwlee84/lazyfind/internal/domain"
)

func realTools(t *testing.T) config.Config {
	t.Helper()
	fd, err := exec.LookPath("fd")
	if err != nil {
		fd, err = exec.LookPath("fdfind")
	}
	if err != nil {
		t.Skip("fd not installed")
	}
	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("rg not installed")
	}
	return config.Config{Tools: config.Tools{FD: fd, RG: rg}, Search: config.Search{MaxResults: 5000, MaxMatches: 50}}
}
func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
func parsed(t *testing.T, root, raw string) domain.QuerySpec {
	t.Helper()
	q, err := domain.ParseQuery(raw, domain.QuerySpec{Roots: []string{root}, Sources: []string{"names", "text"}})
	if err != nil {
		t.Fatal(err)
	}
	return q
}
func findItem(t *testing.T, run domain.Run, name string) domain.Item {
	t.Helper()
	for _, item := range run.Items {
		if filepath.Base(item.RawPath()) == name {
			return item
		}
	}
	t.Fatalf("missing %q in %+v", name, run.Items)
	return domain.Item{}
}
func TestRealToolsMergeAndFindContentOnly(t *testing.T) {
	cfg := realTools(t)
	root := t.TempDir()
	write(t, filepath.Join(root, "needle.txt"), "needle first\nneedle second\n")
	write(t, filepath.Join(root, "other.txt"), "only content needle\n")
	write(t, filepath.Join(root, "binary.bin"), "needle\x00other\n")
	write(t, filepath.Join(root, ".hidden-needle"), "needle")
	if err := os.Mkdir(filepath.Join(root, "needle-dir"), 0700); err != nil {
		t.Fatal(err)
	}
	var events []domain.Event
	run := New(cfg).Execute(context.Background(), parsed(t, root, "needle"), func(e domain.Event) { events = append(events, e) })
	if run.Status != "complete" || len(run.Items) != 3 {
		t.Fatalf("run %+v", run)
	}
	both := findItem(t, run, "needle.txt")
	if !contains(both.Sources, "names") || !contains(both.Sources, "text") || both.MatchCount != 2 {
		t.Fatalf("merged %+v", both)
	}
	other := findItem(t, run, "other.txt")
	if !reflect.DeepEqual(other.Sources, []string{"text"}) {
		t.Fatalf("content-only %+v", other)
	}
	if len(events) < 2 || events[0].Kind != "start" || events[0].Run.ID != run.ID || events[1].Kind != "scope" {
		t.Fatalf("bad startup events %+v", events)
	}
	for _, event := range events {
		if event.Kind == "item" && event.Item.ID == both.ID && len(event.Item.Sources) == 1 {
			if len(event.Item.Matches) > 0 && len(event.Item.Matches) != 2 {
				t.Fatal("published slice mutated")
			}
		}
	}
}
func TestRealToolsPathsAndOverlappingRoots(t *testing.T) {
	cfg := realTools(t)
	root := t.TempDir()
	nested := filepath.Join(root, "sub")
	names := []string{"needle quote'\nline.txt", "-needle.txt"}
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "needle\xff.txt"), []byte("needle\n"), 0600); err == nil {
		names = append(names, "needle\xff.txt")
	} else {
		t.Log("filesystem rejects non-UTF-8 filenames; pure parser coverage remains")
	}
	for _, n := range names {
		write(t, filepath.Join(nested, n), "needle\n")
	}
	q := parsed(t, root, "needle")
	q.Roots = append(q.Roots, nested)
	run := New(cfg).Execute(context.Background(), q, nil)
	if run.Status != "complete" || len(run.Items) != len(names) {
		t.Fatalf("%+v", run)
	}
	for _, n := range names {
		i := findItem(t, run, n)
		if i.MatchCount != 1 {
			t.Fatalf("overlap inflated match count %+v", i)
		}
		if strings.Contains(n, "\xff") && i.PathBytes == "" {
			t.Fatal("raw non UTF-8 path lost")
		}
	}
}
func TestMetadataFiltersAndLiteralFullPath(t *testing.T) {
	cfg := realTools(t)
	root := filepath.Join(t.TempDir(), "needle-root")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "new.md"), "needle 12345")
	write(t, filepath.Join(root, "old.md"), "needle 12345")
	write(t, filepath.Join(root, "tiny.txt"), "needle")
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, "old.md"), old, old); err != nil {
		t.Fatal(err)
	}
	run := New(cfg).Execute(context.Background(), parsed(t, root, "needle ext:md mtime:<1d size:>10"), nil)
	if len(run.Items) != 1 || filepath.Base(run.Items[0].RawPath()) != "new.md" {
		t.Fatalf("filters: %+v", run)
	}
	q := parsed(t, root, "needle-root")
	q.Sources = []string{"names"}
	run = New(cfg).Execute(context.Background(), q, nil)
	if len(run.Items) != 0 {
		t.Fatalf("basename search matched ancestor %+v", run)
	}
	q.FullPath = true
	run = New(cfg).Execute(context.Background(), q, nil)
	if len(run.Items) != 3 {
		t.Fatalf("fullpath failed %+v", run)
	}
}
func TestEmptyQueryDoesNotRunRGAndMatchesAreBounded(t *testing.T) {
	cfg := realTools(t)
	root := t.TempDir()
	write(t, filepath.Join(root, "file.txt"), strings.Repeat("needle\n", 100))
	cfg.Tools.RG = "does-not-exist"
	q := parsed(t, root, "")
	q.Sources = []string{"text"}
	run := New(cfg).Execute(context.Background(), q, nil)
	if run.Status != "complete" || len(run.Items) != 1 || len(run.Problems) != 0 {
		t.Fatalf("empty query %+v", run)
	}
	cfg = realTools(t)
	cfg.Search.MaxMatches = 3
	run = New(cfg).Execute(context.Background(), parsed(t, root, "needle"), nil)
	item := findItem(t, run, "file.txt")
	if len(item.Matches) != 3 || !item.MatchesTruncated || item.MatchCount != 4 {
		t.Fatalf("matches not capped %+v", item)
	}
}
func TestResultLimitAndMissingOptionalSource(t *testing.T) {
	cfg := realTools(t)
	root := t.TempDir()
	for i := 0; i < 10; i++ {
		write(t, filepath.Join(root, fmt.Sprintf("needle-%d", i)), "x")
	}
	cfg.Search.MaxResults = 2
	q := parsed(t, root, "needle")
	q.Sources = []string{"names"}
	run := New(cfg).Execute(context.Background(), q, nil)
	if !run.Truncated || run.Status != "truncated" || len(run.Items) != 2 || run.Observed < 3 {
		t.Fatalf("limit %+v", run)
	}
	cfg.Search.MaxResults = 50
	cfg.Tools.RGA = "does-not-exist"
	q.Sources = []string{"names", "documents"}
	run = New(cfg).Execute(context.Background(), q, nil)
	if run.Status != "partial" || len(run.Items) != 10 || len(run.Problems) != 1 {
		t.Fatalf("missing optional source %+v", run)
	}
}
func TestTimeBoundsInclusiveExclusive(t *testing.T) {
	now := time.Date(2026, 9, 24, 0, 0, 0, 0, time.Local)
	before := now.Add(time.Hour)
	size := int64(20)
	item := domain.Item{Kind: "file", Size: &size, Modified: &now}
	q := domain.QuerySpec{}
	if !accepts(item, q, &now, &before) {
		t.Fatal("lower bound excluded")
	}
	if accepts(item, q, nil, &now) {
		t.Fatal("upper bound included")
	}
	q.Filters.MinSize = &size
	if !accepts(item, q, nil, nil) {
		t.Fatal("inclusive size excluded")
	}
	item.Kind = "directory"
	item.Size = nil
	if accepts(item, q, nil, nil) {
		t.Fatal("directory size invented")
	}
}
func TestReadOversizedRecordContinuesAndLongLinesWork(t *testing.T) {
	input := strings.Repeat("x", 8<<20+1) + "\n" + "{}\n"
	r := bufio.NewReader(strings.NewReader(input))
	if _, err := readRecord(r); !errors.Is(err, errLargeRecord) {
		t.Fatalf("got %v", err)
	}
	record, err := readRecord(r)
	if err != nil || string(record) != "{}\n" {
		t.Fatalf("%q %v", record, err)
	}
	cfg := realTools(t)
	root := t.TempDir()
	write(t, filepath.Join(root, "huge.txt"), strings.Repeat("a", 128<<10)+"needle\n")
	run := New(cfg).Execute(context.Background(), parsed(t, root, "needle"), nil)
	if len(run.Items) != 1 || len(run.Items[0].Matches) == 0 || len(run.Items[0].Matches[0].Text) > 2052 {
		t.Fatalf("long record %+v", run)
	}
}
func TestRGBytesAndLateBinaryFiltering(t *testing.T) {
	cfg := realTools(t)
	root := t.TempDir()
	p := filepath.Join(root, "odd.txt")
	write(t, p, "data")
	p, _ = filepath.EvalSymlinks(p)
	// Fake rg faithfully exercises binary end ordering independently of rg's
	// read buffer size, which otherwise makes late NUL detection nondeterministic.
	fake := filepath.Join(t.TempDir(), "rg")
	events := []map[string]any{{"type": "match", "data": map[string]any{"path": map[string]string{"bytes": base64.StdEncoding.EncodeToString([]byte(p))}, "lines": map[string]string{"bytes": base64.StdEncoding.EncodeToString([]byte("needle\xff\n"))}, "line_number": 2, "submatches": []map[string]int{{"start": 0, "end": 6}}}}, {"type": "end", "data": map[string]any{"path": map[string]string{"bytes": base64.StdEncoding.EncodeToString([]byte(p))}, "binary_offset": 99}}}
	var output strings.Builder
	for _, event := range events {
		b, _ := json.Marshal(event)
		output.Write(b)
		output.WriteByte('\n')
	}
	script := "#!/bin/sh\ncat <<'LAZYFIND_FIXTURE'\n" + output.String() + "LAZYFIND_FIXTURE\n"
	if err := os.WriteFile(fake, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cfg.Tools.RG = fake
	q := parsed(t, root, "needle")
	q.Sources = []string{"text"}
	run := New(cfg).Execute(context.Background(), q, nil)
	if len(run.Items) != 0 || run.Status != "empty" {
		t.Fatalf("binary match escaped %+v", run)
	}
	events[1]["data"].(map[string]any)["binary_offset"] = nil
	output.Reset()
	for _, event := range events {
		b, _ := json.Marshal(event)
		output.Write(b)
		output.WriteByte('\n')
	}
	script = "#!/bin/sh\ncat <<'LAZYFIND_FIXTURE'\n" + output.String() + "LAZYFIND_FIXTURE\n"
	if err := os.WriteFile(fake, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	run = New(cfg).Execute(context.Background(), q, nil)
	if len(run.Items) != 1 || run.Items[0].RawPath() != p || run.Items[0].Matches[0].Line != 2 {
		t.Fatalf("byte path/lines lost %+v", run)
	}
}
func TestRemoteRealToolsThroughFakeSSH(t *testing.T) {
	cfg := realTools(t)
	root := t.TempDir()
	write(t, filepath.Join(root, "quote' line\nneedle.txt"), "needle remote\n")
	fake := filepath.Join(t.TempDir(), "ssh")
	script := `#!/bin/sh
while [ "$#" -gt 0 ]; do if [ "$1" = -- ]; then shift; shift; break; fi; shift; done
exec sh -c "$1"
`
	if err := os.WriteFile(fake, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cfg.Tools.SSH = fake
	q := parsed(t, root, "needle")
	q.Target.Host = "fixture"
	run := New(cfg).Execute(context.Background(), q, nil)
	if run.Status != "complete" || len(run.Items) != 1 || len(run.Items[0].Sources) != 2 || run.Items[0].Modified == nil {
		t.Fatalf("remote fixture %+v", run)
	}
}
func TestCancelSearchRejectsOngoingChildren(t *testing.T) {
	cfg := realTools(t)
	fake := filepath.Join(t.TempDir(), "fd")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nsleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg.Tools.FD = fake
	q := parsed(t, t.TempDir(), "needle")
	q.Sources = []string{"names"}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	var run domain.Run
	go func() {
		defer wg.Done()
		run = New(cfg).Execute(ctx, q, func(e domain.Event) {
			if e.Kind == "scope" {
				cancel()
			}
		})
	}()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("search did not cancel")
	}
	if run.Status != "cancelled" {
		t.Fatalf("%+v", run)
	}
}
func TestRecentAppliesRootIgnoreAndFilters(t *testing.T) {
	cfg := realTools(t)
	root := t.TempDir()
	good := filepath.Join(root, "needle-dir")
	ignored := filepath.Join(root, "ignored-needle")
	outside := filepath.Join(t.TempDir(), "needle-outside")
	for _, p := range []string{good, ignored, outside} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(root, ".ignore"), "ignored-*\n")
	fake := filepath.Join(t.TempDir(), "zoxide")
	script := "#!/bin/sh\ncat <<'LAZYFIND_FIXTURE'\n" + strings.Join([]string{good, ignored, outside}, "\n") + "\nLAZYFIND_FIXTURE\n"
	if err := os.WriteFile(fake, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cfg.Tools.Zoxide = fake
	q := parsed(t, root, "needle type:dir")
	q.Sources = []string{"recent"}
	run := New(cfg).Execute(context.Background(), q, nil)
	physicalGood, _ := filepath.EvalSymlinks(good)
	if len(run.Items) != 1 || run.Items[0].RawPath() != physicalGood {
		t.Fatalf("recent %+v", run)
	}
}

func TestNonUTF8MetadataPreservesRawPath(t *testing.T) {
	path := "/root/odd\xff.txt"
	item := newItem(domain.Target{}, path, "file", 1, time.Now())
	if item.PathBytes == "" || item.RawPath() != path || item.Path == path {
		t.Fatalf("invalid byte path lost: %+v", item)
	}
}

func TestDocumentAdapterMarksExtractedLinesAndSkipsPlainText(t *testing.T) {
	cfg := realTools(t)
	root := t.TempDir()
	p := filepath.Join(root, "document.pdf")
	write(t, p, "%PDF binary\x00")
	write(t, filepath.Join(root, "plain.txt"), "needle plain")
	p, _ = filepath.EvalSymlinks(p)
	fake := filepath.Join(t.TempDir(), "rga")
	event := map[string]any{"type": "match", "data": map[string]any{"path": map[string]string{"text": p}, "lines": map[string]string{"text": "needle extracted\n"}, "line_number": 7, "submatches": []map[string]int{{"start": 0, "end": 6}}}}
	end := map[string]any{"type": "end", "data": map[string]any{"path": map[string]string{"text": p}, "binary_offset": nil}}
	b, _ := json.Marshal(event)
	e, _ := json.Marshal(end)
	script := "#!/bin/sh\nfor arg do case \"$arg\" in *plain.txt) exit 9;; esac; done\ncat <<'LAZYFIND_FIXTURE'\n" + string(b) + "\n" + string(e) + "\nLAZYFIND_FIXTURE\n"
	if err := os.WriteFile(fake, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cfg.Tools.RGA = fake
	q := parsed(t, root, "needle")
	q.Sources = []string{"text", "documents"}
	run := New(cfg).Execute(context.Background(), q, nil)
	if run.Status != "complete" || len(run.Items) != 2 {
		t.Fatalf("document run %+v", run)
	}
	item := findItem(t, run, "document.pdf")
	if len(item.Matches) != 1 || !item.Matches[0].Extracted || item.Matches[0].Line != 7 || !reflect.DeepEqual(item.Sources, []string{"documents"}) {
		t.Fatalf("document locator %+v", item)
	}
}

func TestDirectoryFilterDoesNotRequireContentTools(t *testing.T) {
	cfg := realTools(t)
	cfg.Tools.RG = "does-not-exist"
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "needle-dir"), 0700); err != nil {
		t.Fatal(err)
	}
	run := New(cfg).Execute(context.Background(), parsed(t, root, "needle type:dir"), nil)
	if run.Status != "complete" || len(run.Items) != 1 || len(run.Problems) != 0 {
		t.Fatalf("unneeded rg prevented directory search: %+v", run)
	}
}
func TestGNUStatProtocolPreservesNULPathsAndKinds(t *testing.T) {
	dir := t.TempDir()
	stat := filepath.Join(dir, "stat")
	script := `#!/bin/sh
[ "$1" = -c ] || exit 7
for last do :; done
case "$last" in
 /) printf '41ed 4096 2023-11-15 06:13:20.123456789 +0800\n';;
 */directory) printf '41ed 4096 2023-11-15 06:13:20.123456789 +0800\n';;
 */link) printf 'a1ff 23 2023-11-15 06:13:20.123456789 +0800\n';;
 *) printf '81a4 42 2023-11-15 06:13:20.123456789 +0800\n';;
esac
`
	if err := os.WriteFile(stat, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ssh := filepath.Join(dir, "ssh")
	sshScript := `#!/bin/sh
while [ "$#" -gt 0 ]; do if [ "$1" = -- ]; then shift; shift; break; fi; shift; done
exec sh -c "$1"
`
	if err := os.WriteFile(ssh, []byte(sshScript), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	service := New(config.Config{Tools: config.Tools{SSH: ssh}})
	for _, test := range []struct {
		p, kind string
		size    int64
	}{{"/fixture/quote'\nfile", "file", 42}, {"/fixture/directory", "directory", 0}, {"/fixture/link", "symlink", 0}} {
		item, err := service.Inspect(context.Background(), domain.Target{Host: "fixture"}, test.p)
		if err != nil || item.Kind != test.kind || item.RawPath() != test.p || item.Modified == nil || item.Modified.Unix() != 1700000000 || item.Modified.Nanosecond() != 123456789 {
			t.Fatalf("metadata %+v %v", item, err)
		}
		if test.kind == "file" && (item.Size == nil || *item.Size != test.size) {
			t.Fatalf("size %+v", item)
		}
		if test.kind != "file" && item.Size != nil {
			t.Fatal("invented directory/link content size")
		}
	}
}

func TestRemoteRecentResolvesLearnedSymlinkPathsOnTarget(t *testing.T) {
	cfg := realTools(t)
	root := t.TempDir()
	actual := filepath.Join(root, "needle-dir")
	if err := os.Mkdir(actual, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	zoxide := filepath.Join(t.TempDir(), "zoxide")
	script := "#!/bin/sh\ncat <<'LAZYFIND_FIXTURE'\n" + filepath.Join(alias, "needle-dir") + "\nLAZYFIND_FIXTURE\n"
	if err := os.WriteFile(zoxide, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cfg.Tools.Zoxide = zoxide
	ssh := filepath.Join(t.TempDir(), "ssh")
	script = `#!/bin/sh
while [ "$#" -gt 0 ]; do if [ "$1" = -- ]; then shift; shift; break; fi; shift; done
exec sh -c "$1"
`
	if err := os.WriteFile(ssh, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cfg.Tools.SSH = ssh
	q := parsed(t, root, "needle")
	q.Target.Host = "fixture"
	q.Sources = []string{"recent"}
	run := New(cfg).Execute(context.Background(), q, nil)
	want, _ := filepath.EvalSymlinks(actual)
	if run.Status != "complete" || len(run.Items) != 1 || run.Items[0].RawPath() != want {
		t.Fatalf("remote learned alias lost %+v", run)
	}
}
