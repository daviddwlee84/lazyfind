package actions

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/daviddwlee84/lazyfind/internal/config"
	"github.com/daviddwlee84/lazyfind/internal/domain"
)

func fileItem(t *testing.T, name string) domain.Item {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("hello\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return domain.Item{ID: domain.ItemID(domain.Target{}, path), Path: path, Name: name, Kind: "file"}
}
func TestRulesAccumulateAndFirstDefaultWins(t *testing.T) {
	item := fileItem(t, "note.md")
	cfg := config.Config{Actions: []config.Action{
		{ID: "first", Argv: []string{"sh", "-c", "exit 0"}},
		{ID: "second", Argv: []string{"sh", "-c", "exit 0"}},
		{ID: "reader", Argv: []string{"sh", "-c", "exit 0"}, Mode: "preview"},
		{ID: "missing", Argv: []string{filepath.Join(t.TempDir(), "not-installed")}},
	}, Rules: []config.Rule{
		{Extensions: []string{"md"}, Actions: []string{"first", "missing"}, Default: "first", Preview: "reader"},
		{MIME: "text/*", Actions: []string{"first", "second"}, Default: "second", Preview: "bat"},
	}}
	r, err := New(cfg).Resolve(context.Background(), item, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Default != "first" || r.Preview != "reader" {
		t.Fatalf("wrong defaults: %+v", r)
	}
	counts := map[string]int{}
	for _, a := range r.Actions {
		counts[a.ID]++
	}
	if counts["first"] != 1 || counts["second"] != 1 {
		t.Fatalf("actions not unioned: %v", counts)
	}
	missing, _ := r.Find("missing")
	if missing.Available || !strings.Contains(missing.Reason, "unavailable") {
		t.Fatalf("missing tool not explained: %+v", missing)
	}
}
func TestTypedArgumentsAndEditorParsing(t *testing.T) {
	item := fileItem(t, "quote' newline\n$HOME {query}.txt")
	item.Matches = []domain.Match{{Line: 12}, {Line: 99, Extracted: true}}
	query := "$(touch /tmp/never-lazyfind) ' ; echo injected"
	argv, err := Expand([]string{"--file={path}", "{query}", "+{line}"}, item, query, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--file=" + item.Path, query, "+12"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("arguments changed: %#v", argv)
	}
	t.Setenv("VISUAL", `"/path with spaces/editor" --wait 'literal $HOME'`)
	a := New(config.Config{}).builtins(item)["editor"]
	if !reflect.DeepEqual(a.Argv, []string{"/path with spaces/editor", "--wait", "literal $HOME", "{path}"}) {
		t.Fatalf("incorrect editor argv: %#v", a.Argv)
	}
	if _, err = Expand([]string{"{git_root}"}, item, "", ""); err == nil {
		t.Fatal("missing git context should fail")
	}
	if _, err = Expand([]string{"{unknown}"}, item, "", ""); err == nil {
		t.Fatal("unknown placeholder should fail")
	}
}
func TestPrepareDoesNotExecuteShellTextAndRejectsMissingItem(t *testing.T) {
	item := fileItem(t, "a'$(echo nope).txt")
	cfg := config.Config{Actions: []config.Action{{ID: "capture", Argv: []string{"printf", "%s\\n", "{path}", "{query}"}, Mode: "preview"}}, Rules: []config.Rule{{Actions: []string{"capture"}}}}
	s := New(cfg)
	query := "hello; $(echo injected)"
	cmd, err := s.Prepare(context.Background(), item, query, "capture")
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != item.Path+"\n"+query+"\n" {
		t.Fatalf("argument boundary lost: %q", out)
	}
	if err = os.Remove(item.Path); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Prepare(context.Background(), item, query, "capture"); err == nil {
		t.Fatal("missing item was accepted")
	}
}
func TestGitWorktreeAndBareRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	run := func(argv ...string) {
		t.Helper()
		cmd := exec.Command("git", argv...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=lazyfind-test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=lazyfind-test", "GIT_COMMITTER_EMAIL=test@example.invalid", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", argv, out, err)
		}
	}
	repo := filepath.Join(root, "repo")
	run("init", "-q", repo)
	run("-C", repo, "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "initial")
	worktree := filepath.Join(root, "worktree")
	run("-C", repo, "worktree", "add", "--detach", worktree)
	bare := filepath.Join(root, "bare.git")
	run("init", "--bare", "-q", bare)
	for _, path := range []string{repo, worktree, bare} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			item := domain.Item{Path: path, Kind: "directory"}
			got := New(config.Config{}).gitRoot(context.Background(), item)
			canonical, err := filepath.EvalSymlinks(path)
			if err != nil {
				t.Fatal(err)
			}
			if got != canonical {
				t.Fatalf("git root=%q want %q", got, canonical)
			}
		})
	}
}
func TestRemoteHandoffUsesNativePTYAndNoLocalOpen(t *testing.T) {
	item := fileItem(t, "a'b.txt")
	item.Target = domain.Target{Host: "example"}
	ssh := filepath.Join(t.TempDir(), "fake-ssh")
	if err := os.WriteFile(ssh, []byte("#!/bin/sh\nfor arg do command=$arg; done\nexec /bin/sh -c \"$command\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Tools: config.Tools{SSH: ssh}, Actions: []config.Action{{ID: "capture", Argv: []string{"printf", "%s", "{path}"}, Mode: "suspend"}}, Rules: []config.Rule{{Actions: []string{"capture", "open"}}}}
	s := New(cfg)
	r, err := s.Resolve(context.Background(), item, "")
	if err != nil {
		t.Fatal(err)
	}
	opener, _ := r.Find("open")
	if opener.Available {
		t.Fatal("remote item enabled local opener")
	}
	cmd, err := s.Prepare(context.Background(), item, "", "capture")
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(cmd.Args, " ")
	if !strings.Contains(args, "-t") || strings.Contains(args, "BatchMode") {
		t.Fatalf("wrong SSH terminal mode: %v", cmd.Args)
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != item.Path {
		t.Fatalf("remote quoting lost: %q", out)
	}
}
func TestSplitCommandDoesNotExpandAndRejectsBrokenQuotes(t *testing.T) {
	got, err := SplitCommand(`editor --wait "two words" '$HOME' "$(uname)"`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"editor", "--wait", "two words", "$HOME", "$(uname)"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v", got)
	}
	if _, err = SplitCommand(`editor "unfinished`); err == nil {
		t.Fatal("bad quote accepted")
	}
}

func TestExplicitShellSyntaxAndDataArguments(t *testing.T) {
	item := fileItem(t, "special' file.txt")
	args := []string{"sh", "-c", `printf '%s' "${HOME}"; awk '{ print $1 }' "$1"`, "lazyfind", "{path}"}
	got, err := Expand(args, item, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got[2] != args[2] || got[4] != item.Path {
		t.Fatalf("explicit script syntax changed: %#v", got)
	}
}

func TestDefaultEditorUsesSelectedNativeLineOnly(t *testing.T) {
	item := fileItem(t, "selected.txt")
	t.Setenv("VISUAL", "nvim --clean")
	item.Matches = []domain.Match{{Line: 42}, {Line: 7}}
	s := New(config.Config{})
	argv := s.builtins(item)["editor"].Argv
	if !reflect.DeepEqual(argv, []string{"nvim", "--clean", "+42", "{path}"}) {
		t.Fatalf("editor did not follow selected hit: %#v", argv)
	}
	item.Matches = []domain.Match{{Line: 99, Extracted: true}, {Line: 42}}
	argv = s.builtins(item)["editor"].Argv
	if !reflect.DeepEqual(argv, []string{"nvim", "--clean", "{path}"}) {
		t.Fatalf("extracted location became file line: %#v", argv)
	}
	expanded, err := Expand([]string{"{line}"}, item, "", "")
	if err != nil || expanded[0] != "1" {
		t.Fatalf("extracted line placeholder: %v %v", expanded, err)
	}
}
