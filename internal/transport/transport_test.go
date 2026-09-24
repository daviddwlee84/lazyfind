package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/lazyfind/internal/config"
	"github.com/daviddwlee84/lazyfind/internal/domain"
)

func fakeSSH(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ssh")
	script := `#!/bin/sh
while [ "$#" -gt 0 ]; do
 if [ "$1" = -- ]; then shift; alias=$1; shift; break; fi
 shift
done
[ "$alias" = fixture ] || exit 255
exec sh -c "$1"
`
	if err := os.WriteFile(p, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestSSHArgvQuotingPreservesBytes(t *testing.T) {
	r := New(config.Config{Tools: config.Tools{SSH: fakeSSH(t)}})
	cmd, err := r.Command(context.Background(), domain.Target{Host: "fixture"}, []string{"true"}, "")
	if err != nil || !strings.Contains(strings.Join(cmd.Args, " "), " -T ") {
		t.Fatalf("background SSH can allocate a TTY: %v %v", cmd, err)
	}
	args := []string{"printf", "%s\\000", "space name", "a'b", "line\nbreak", "$HOME", "$(touch SHOULD_NOT_EXIST)", "`id`", "-x", ""}
	got, err := r.Run(context.Background(), domain.Target{Host: "fixture"}, args, "")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join(args[2:], "\x00") + "\x00"
	if string(got) != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if _, err = r.Command(context.Background(), domain.Target{Host: "-oProxyCommand=bad"}, []string{"true"}, ""); err == nil {
		t.Fatal("accepted option alias")
	}
	if _, err = r.Command(context.Background(), domain.Target{}, []string{"printf", "a\x00b"}, ""); err == nil {
		t.Fatal("accepted NUL")
	}
}
func TestResolveRootsLocallyAndOnSSH(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "space ' quote\nline")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", root)
	physicalRoot, _ := filepath.EvalSymlinks(root)
	physicalNested, _ := filepath.EvalSymlinks(nested)
	r := New(config.Config{Tools: config.Tools{SSH: fakeSSH(t)}})
	for _, target := range []domain.Target{{}, {Host: "fixture"}} {
		roots, err := r.ResolveRoots(context.Background(), target, []string{"~", "~/space ' quote\nline", root})
		if err != nil {
			t.Fatal(err)
		}
		if len(roots) != 2 || roots[0] != physicalRoot || roots[1] != physicalNested {
			t.Fatalf("%+v: %q", target, roots)
		}
	}
}
func TestStreamCancellationTerminatesSubprocess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := New(config.Config{})
	started := time.Now()
	err := r.Stream(ctx, domain.Target{}, []string{"sh", "-c", "printf ready; sleep 30 & wait"}, "", func(rd io.Reader) error {
		b := make([]byte, 5)
		if _, err := io.ReadFull(rd, b); err != nil {
			return err
		}
		cancel()
		_, err := io.Copy(io.Discard, rd)
		return err
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("cancellation waited for descendant")
	}
}
func TestStreamReadErrorTerminatesChild(t *testing.T) {
	expected := errors.New("consumer stopped")
	start := time.Now()
	err := New(config.Config{}).Stream(context.Background(), domain.Target{}, []string{"sh", "-c", "sleep 30"}, "", func(io.Reader) error { return expected })
	if !errors.Is(err, expected) {
		t.Fatalf("got %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("reader cancellation was slow")
	}
}
func TestRunAndExitStatus(t *testing.T) {
	r := New(config.Config{})
	out, err := r.Run(context.Background(), domain.Target{}, []string{"sh", "-c", "printf result; printf diagnostic >&2; exit 7"}, "")
	if !bytes.Equal(out, []byte("result")) || ExitCode(err) != 7 || !strings.Contains(err.Error(), "diagnostic") {
		t.Fatalf("%q %v", out, err)
	}
	if !r.Available(context.Background(), domain.Target{}, "sh") || r.Available(context.Background(), domain.Target{}, "lazyfind-nonexistent-binary") {
		t.Fatal("bad executable detection")
	}
	cmd, err := r.TerminalCommand(context.Background(), domain.Target{Host: "fixture"}, []string{"vi", "x"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(cmd.Args, " "), "-t") || strings.Contains(strings.Join(cmd.Args, " "), "BatchMode") {
		t.Fatalf("wrong terminal args %q", cmd.Args)
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh unavailable")
	}
}

func TestTildeNamedDirectoryIsNotConfusedWithHome(t *testing.T) {
	home := t.TempDir()
	tilde := filepath.Join(home, "~")
	if err := os.Mkdir(tilde, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	got, err := New(config.Config{}).ResolveRoots(context.Background(), domain.Target{}, []string{"~/~"})
	want, _ := filepath.EvalSymlinks(tilde)
	if err != nil || len(got) != 1 || got[0] != want {
		t.Fatalf("%q %v", got, err)
	}
}
