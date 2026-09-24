package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/lazyfind/internal/cache"
	"github.com/daviddwlee84/lazyfind/internal/config"
	"github.com/daviddwlee84/lazyfind/internal/domain"
	"github.com/daviddwlee84/lazyfind/internal/history"
)

func isolateCLI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for key, suffix := range map[string]string{"HOME": "home", "XDG_CONFIG_HOME": "config", "XDG_STATE_HOME": "state", "XDG_CACHE_HOME": "cache", "XDG_DATA_HOME": "data"} {
		t.Setenv(key, filepath.Join(dir, suffix))
	}
	t.Setenv("LAZYFIND_CONFIG", "")
	return dir
}

func invoke(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	c := New("test")
	var out, diagnostics bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&diagnostics)
	c.SetArgs(args)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := c.ExecuteContext(ctx)
	return out.String(), diagnostics.String(), err
}

func decodeOnlyJSON[T any](t *testing.T, value string) T {
	t.Helper()
	var result T
	decoder := json.NewDecoder(strings.NewReader(value))
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, value)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("extra stdout after JSON: %v (%v)", extra, err)
	}
	if strings.Contains(value, "\x1b[") {
		t.Fatal("ANSI escape in JSON output")
	}
	return result
}

func requireFindTools(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("fd"); err != nil {
		if _, err = exec.LookPath("fdfind"); err != nil {
			t.Skip("fd/fdfind is needed for this real-tool integration test")
		}
	}
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg is needed for this real-tool integration test")
	}
}

func searchFixture(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "root with spaces")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"needle.txt": "needle is in both the name and contents\n", "contents-only.txt": "find the needle here\n", "unrelated.txt": "nothing relevant\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestSearchJSONIsPureAndMergesRealToolResults(t *testing.T) {
	isolateCLI(t)
	requireFindTools(t)
	root := searchFixture(t)
	out, diagnostics, err := invoke(t, "search", "needle", "--root", root, "--source", "names,text", "--json")
	if err != nil {
		t.Fatalf("search failed: %v\n%s\n%s", err, out, diagnostics)
	}
	if diagnostics != "" {
		t.Fatalf("successful search emitted diagnostics: %s", diagnostics)
	}
	run := decodeOnlyJSON[domain.Run](t, out)
	if run.SchemaVersion != 1 || run.Status != "complete" || len(run.Items) != 2 {
		t.Fatalf("unexpected envelope: %+v", run)
	}
	seen := map[string]domain.Item{}
	for _, item := range run.Items {
		seen[item.Name] = item
	}
	both, ok := seen["needle.txt"]
	if !ok || len(both.Sources) != 2 {
		t.Fatalf("name/content duplicate not merged: %+v", seen)
	}
	content, ok := seen["contents-only.txt"]
	if !ok || len(content.Matches) == 0 || content.Matches[0].Line != 1 {
		t.Fatalf("content-only result lost: %+v", seen)
	}
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(cfg.Paths.State); !os.IsNotExist(err) {
		t.Fatalf("unsaved search created persistent state: %v", err)
	}
}

func TestNoMatchesSucceedsWithAnEmptyJSONResult(t *testing.T) {
	isolateCLI(t)
	requireFindTools(t)
	root := searchFixture(t)
	out, diagnostics, err := invoke(t, "search", "does-not-exist-941", "--root", root, "--json")
	if err != nil {
		t.Fatalf("empty search should exit 0: %v %s", err, diagnostics)
	}
	run := decodeOnlyJSON[domain.Run](t, out)
	if run.Status != "empty" || len(run.Items) != 0 || len(run.Problems) != 0 {
		t.Fatalf("unexpected empty result: %+v", run)
	}
}

func TestArgumentCountFailuresAreUsageErrors(t *testing.T) {
	isolateCLI(t)
	for _, args := range [][]string{{"search", "one", "two"}, {"history", "show"}, {"pick", "extra"}, {"cache", "clear", "extra"}} {
		_, _, err := invoke(t, args...)
		if err == nil || ExitCode(err) != 2 {
			t.Fatalf("%v: %v (code %d)", args, err, ExitCode(err))
		}
	}
}

func TestInvalidQueriesConfigAndExplicitEmptySourcesExitTwo(t *testing.T) {
	dir := isolateCLI(t)
	badConfig := filepath.Join(dir, "broken.toml")
	if err := os.WriteFile(badConfig, []byte("[search\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"kind", []string{"search", "needle type:spaceship", "--json"}},
		{"date", []string{"search", "needle after:wrong", "--json"}},
		{"source", []string{"search", "needle", "--source", "unknown", "--json"}},
		{"empty-source", []string{"search", "needle", "--source=", "--json"}},
		{"query-twice", []string{"search", "needle", "--query", "again", "--json"}},
		{"config", []string{"--config", badConfig, "search", "needle", "--json"}},
		{"flag", []string{"search", "needle", "--nonexistent-flag"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, _, err := invoke(t, tc.args...)
			if err == nil || ExitCode(err) != 2 {
				t.Fatalf("want usage exit 2, got %v (code %d)", err, ExitCode(err))
			}
			if out != "" {
				t.Fatalf("invalid request wrote stdout: %q", out)
			}
		})
	}
}

func TestNonTTYInvocationAndPositionalRootsHaveActionableErrors(t *testing.T) {
	isolateCLI(t)
	root := searchFixture(t)
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdin
	os.Stdin = null
	t.Cleanup(func() { os.Stdin = original; _ = null.Close() })
	for _, args := range [][]string{{}, {root}, {"pick"}} {
		out, _, err := invoke(t, args...)
		if err == nil || ExitCode(err) != 2 || !strings.Contains(err.Error(), "terminal") {
			t.Fatalf("args=%q: want terminal usage error, got %v (code %d)", args, err, ExitCode(err))
		}
		if strings.Contains(err.Error(), "unknown command") {
			t.Fatalf("positional root was parsed as a subcommand: %v", err)
		}
		if out != "" {
			t.Fatalf("TUI error polluted stdout: %q", out)
		}
	}
}

func TestSaveHistoryAndRerunRelativeDatesCreatesLinkedSnapshot(t *testing.T) {
	isolateCLI(t)
	requireFindTools(t)
	root := searchFixture(t)
	out, diagnostics, err := invoke(t, "search", "needle mtime:<7d", "--root", root, "--source", "names", "--save", "--json")
	if err != nil {
		t.Fatalf("save search: %v %s", err, diagnostics)
	}
	first := decodeOnlyJSON[domain.Run](t, out)
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	store := history.New(cfg)
	saved, err := store.Get(context.Background(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Query.Filters.ModifiedWithin != "7d" || len(saved.Items) != 1 {
		t.Fatalf("query/results not captured: %+v", saved)
	}
	// Keep an old fixed snapshot with the same relative query. Rerunning it
	// must resolve its window at the new run's start, not reuse the old dates.
	old := saved
	old.ID = "older-relative-query"
	old.StartedAt = time.Now().AddDate(0, 0, -30)
	old.CapturedAt = old.StartedAt
	oldAfter := old.StartedAt.Add(-7 * 24 * time.Hour)
	old.ResolvedAfter = &oldAfter
	if err = store.Save(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	out, diagnostics, err = invoke(t, "history", "rerun", old.ID, "--json")
	if err != nil {
		t.Fatalf("rerun: %v %s", err, diagnostics)
	}
	rerun := decodeOnlyJSON[domain.Run](t, out)
	if rerun.ID == old.ID || rerun.ParentID != old.ID || rerun.ResolvedAfter == nil {
		t.Fatalf("rerun identity/bounds missing: %+v", rerun)
	}
	want := rerun.StartedAt.Add(-7 * 24 * time.Hour)
	if !rerun.ResolvedAfter.Equal(want) {
		t.Fatalf("relative time not recomputed: got %v, want %v", rerun.ResolvedAfter, want)
	}
	previous, err := store.Get(context.Background(), old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !previous.ResolvedAfter.Equal(oldAfter) || !previous.CapturedAt.Equal(old.CapturedAt) {
		t.Fatal("rerun modified its original snapshot")
	}
	if _, err = store.Get(context.Background(), rerun.ID); err != nil {
		t.Fatalf("rerun was not persisted: %v", err)
	}
	out, _, err = invoke(t, "history", "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	summaries := decodeOnlyJSON[[]domain.Run](t, out)
	if len(summaries) != 3 {
		t.Fatalf("expected 3 distinct runs, got %d", len(summaries))
	}
}

func TestCacheClearLeavesHistoricalResultsAvailableOffline(t *testing.T) {
	isolateCLI(t)
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	run := domain.Run{SchemaVersion: 1, ID: "offline", Status: "complete", StartedAt: now, CapturedAt: now, Query: domain.QuerySpec{Raw: "needle", Target: domain.Target{Host: "unreachable.example"}, Roots: []string{"/remote/missing"}}, Items: []domain.Item{{ID: "archived-item", Path: "/remote/missing/needle.txt", Target: domain.Target{Host: "unreachable.example"}}}}
	if err = history.New(cfg).Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err = cache.New(cfg).Put("preview", "saved preview"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = invoke(t, "cache", "clear"); err != nil {
		t.Fatal(err)
	}
	stats, err := cache.New(cfg).Stats()
	if err != nil || stats.Entries != 0 {
		t.Fatalf("cache remains: %+v %v", stats, err)
	}
	out, diagnostics, err := invoke(t, "history", "show", run.ID, "--json")
	if err != nil {
		t.Fatalf("offline history failed: %v %s", err, diagnostics)
	}
	got := decodeOnlyJSON[domain.Run](t, out)
	if got.ID != run.ID || len(got.Items) != 1 || got.Items[0].Path != run.Items[0].Path {
		t.Fatalf("cache clear damaged history: %+v", got)
	}
}

func TestConfigPathHelpAndEditHelpDoNotParseOrCreateState(t *testing.T) {
	dir := isolateCLI(t)
	bad := filepath.Join(dir, "broken.toml")
	original := []byte("unclosed = [\n")
	if err := os.WriteFile(bad, original, 0600); err != nil {
		t.Fatal(err)
	}
	out, _, err := invoke(t, "--config", bad, "config", "path")
	if err != nil || strings.TrimSpace(out) != bad {
		t.Fatalf("config path should work with malformed content: %q %v", out, err)
	}
	for _, args := range [][]string{{"--config", bad, "--help"}, {"--config", bad, "config", "edit", "--help"}, {"--config", filepath.Join(dir, "missing.toml"), "search", "--help"}, {"--config", bad, "--version"}} {
		if _, _, err = invoke(t, args...); err != nil {
			t.Fatalf("help/version read config: %q: %v", args, err)
		}
	}
	contents, err := os.ReadFile(bad)
	if err != nil || !bytes.Equal(contents, original) {
		t.Fatal("help changed malformed config")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "broken.toml" {
		t.Fatalf("read-only config commands created state: %v", entries)
	}
}
