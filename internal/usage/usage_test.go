package usage

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daviddwlee84/lazyfind/internal/config"
	"github.com/daviddwlee84/lazyfind/internal/domain"
	"github.com/daviddwlee84/lazyfind/internal/transport"
)

type runnerFunc func(context.Context, domain.Target, []string, string) ([]byte, error)

func (f runnerFunc) Run(ctx context.Context, target domain.Target, argv []string, cwd string) ([]byte, error) {
	return f(ctx, target, argv, cwd)
}

func directory(path string) domain.Item {
	return domain.Item{ID: domain.ItemID(domain.Target{}, path), Path: path, Kind: "directory"}
}
func testService(t *testing.T) *Service {
	t.Helper()
	cfg := config.Defaults()
	cfg.DirectoryUsage.Concurrency = 2
	cfg.DirectoryUsage.TimeoutSeconds = 60
	return New(cfg)
}

func TestKiBParsingChecksUnitsOverflowAndSingleRecord(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		want        int64
		bad         bool
	}{
		{"normal", "12\t.\n", 12 * 1024, false},
		{"zero", "0\t.\n", 0, false},
		{"maximum", fmt.Sprintf("%d\t.\n", math.MaxInt64/1024), math.MaxInt64 / 1024 * 1024, false},
		{"overflow", fmt.Sprintf("%d\t.\n", math.MaxInt64/1024+1), 0, true},
		{"uint-overflow", "18446744073709551616\t.\n", 0, true},
		{"negative", "-1\t.\n", 0, true},
		{"fraction", "1.2\t.\n", 0, true},
		{"missing", "", 0, true},
		{"multiple", "1\t.\n2\t.\n", 0, true},
		{"unexpected-path", "1\tother\n", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseKiB([]byte(tc.input))
			if (err != nil) != tc.bad || got != tc.want {
				t.Fatalf("got %d/%v, want %d bad=%v", got, err, tc.want, tc.bad)
			}
		})
	}
}

func TestMeasurementClassifiesPartialFailureAndInvalidInput(t *testing.T) {
	for _, tc := range []struct {
		name, out string
		err       error
		status    string
		want      int64
	}{
		{"complete", "23\t.\n", nil, "complete", 23 * 1024},
		{"partial", "23\t.\n", errors.New("permission denied"), "partial", 23 * 1024},
		{"command-failed", "", errors.New("du unavailable"), "failed", 0},
		{"malformed", "oops", nil, "failed", 0},
		{"overflow", fmt.Sprintf("%d\t.\n", math.MaxInt64/1024+1), nil, "failed", 0},
		{"canceled-with-total", "23\t.\n", context.Canceled, "canceled", 23 * 1024},
		{"timeout-with-total", "23\t.\n", context.DeadlineExceeded, "timeout", 23 * 1024},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testService(t)
			s.runner = runnerFunc(func(context.Context, domain.Target, []string, string) ([]byte, error) { return []byte(tc.out), tc.err })
			r := s.Measure(context.Background(), directory("/fixture"), false)
			if r.Status != tc.status || r.Bytes != tc.want || r.MeasuredAt.IsZero() {
				t.Fatalf("unexpected measurement: %+v", r)
			}
			if tc.status != "complete" && r.Error == "" {
				t.Fatal("failed/incomplete measurement has no explanation")
			}
		})
	}
	s := testService(t)
	s.runner = runnerFunc(func(context.Context, domain.Target, []string, string) ([]byte, error) {
		t.Fatal("non-directory input launched du")
		return nil, nil
	})
	for _, kind := range []string{"file", "symlink", ""} {
		item := directory("/fixture")
		item.Kind = kind
		if r := s.Measure(context.Background(), item, false); r.Status != "failed" {
			t.Fatalf("kind %q accepted: %+v", kind, r)
		}
	}
}

func TestCompleteOnlyCacheAndForceRefresh(t *testing.T) {
	s := testService(t)
	calls := 0
	output := "1\t.\n"
	var commandError error
	s.runner = runnerFunc(func(context.Context, domain.Target, []string, string) ([]byte, error) {
		calls++
		return []byte(output), commandError
	})
	item := directory("/fixture")
	first := s.Measure(context.Background(), item, false)
	second := s.Measure(context.Background(), item, false)
	if calls != 1 || first.Cached || !second.Cached || !first.MeasuredAt.Equal(second.MeasuredAt) {
		t.Fatalf("cache behavior: calls=%d first=%+v second=%+v", calls, first, second)
	}
	output = "2\t.\n"
	fresh := s.Measure(context.Background(), item, true)
	if calls != 2 || fresh.Cached || fresh.Bytes != 2048 {
		t.Fatalf("force did not remeasure: %+v calls=%d", fresh, calls)
	}
	commandError = errors.New("partial traversal")
	partial := s.Measure(context.Background(), item, true)
	again := s.Measure(context.Background(), item, false)
	if calls != 4 || partial.Status != "partial" || again.Cached || again.Status != "partial" {
		t.Fatalf("incomplete result was cached or stale complete survived force: %+v %+v calls=%d", partial, again, calls)
	}
	commandError = nil
	_ = s.Measure(context.Background(), item, false)
	item.Target = domain.Target{Host: "other-target"}
	_ = s.Measure(context.Background(), item, false)
	if calls != 6 {
		t.Fatal("cache confused identical paths on different targets")
	}
}

func TestMeasurementUsesRawPathAndPortableCommand(t *testing.T) {
	s := testService(t)
	raw := "/fixture/\xff 'quote'\n-name"
	item := directory("display path")
	item.PathBytes = base64.StdEncoding.EncodeToString([]byte(raw))
	item.Target = domain.Target{Host: "fixture-host"}
	s.runner = runnerFunc(func(ctx context.Context, target domain.Target, argv []string, cwd string) ([]byte, error) {
		if cwd != raw || target.Host != "fixture-host" || strings.Join(argv, "\x00") != "env\x00LC_ALL=C\x00du\x00-skP\x00." {
			t.Fatalf("incorrect invocation: %q %q %q", target.Host, argv, cwd)
		}
		return []byte("4\t.\n"), nil
	})
	if r := s.Measure(context.Background(), item, false); r.Status != "complete" || r.ItemID != item.ID {
		t.Fatalf("measurement failed: %+v", r)
	}
}

func TestBatchAndIndividualCallsShareConcurrencyLimit(t *testing.T) {
	s := testService(t)
	var active, maximum, calls atomic.Int32
	started := make(chan struct{}, 10)
	release := make(chan struct{})
	s.runner = runnerFunc(func(ctx context.Context, _ domain.Target, _ []string, _ string) ([]byte, error) {
		current := active.Add(1)
		defer active.Add(-1)
		calls.Add(1)
		for {
			old := maximum.Load()
			if current <= old || maximum.CompareAndSwap(old, current) {
				break
			}
		}
		started <- struct{}{}
		select {
		case <-release:
			return []byte("1\t.\n"), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	items := make([]domain.Item, 9)
	for n := range items {
		items[n] = directory(fmt.Sprintf("/fixture/%d", n))
	}
	done := make(chan []Result, 1)
	go func() {
		var results []Result
		s.MeasureBatch(context.Background(), items, false, func(r Result) { results = append(results, r) })
		done <- results
	}()
	for n := 0; n < 2; n++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("workers did not start")
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("started %d workers above limit", calls.Load())
	}
	individual := make(chan Result, 1)
	go func() { individual <- s.Measure(context.Background(), directory("/fixture/individual"), false) }()
	close(release)
	results := <-done
	if r := <-individual; r.Status != "complete" {
		t.Fatalf("individual measurement failed: %+v", r)
	}
	if len(results) != len(items) || maximum.Load() != 2 || calls.Load() != int32(len(items)+1) {
		t.Fatalf("batch counts: results=%d peak=%d calls=%d", len(results), maximum.Load(), calls.Load())
	}
	for _, r := range results {
		if r.Status != "complete" {
			t.Fatalf("unexpected result: %+v", r)
		}
	}
}

func TestCancellationTimeoutAndWaitingSlots(t *testing.T) {
	t.Run("cancel-running", func(t *testing.T) {
		s := testService(t)
		started := make(chan struct{})
		s.runner = runnerFunc(func(ctx context.Context, _ domain.Target, _ []string, _ string) ([]byte, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan Result, 1)
		go func() { done <- s.Measure(ctx, directory("/fixture"), false) }()
		<-started
		cancel()
		if r := <-done; r.Status != "canceled" {
			t.Fatalf("wrong canceled status: %+v", r)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		s := testService(t)
		s.timeout = 20 * time.Millisecond
		s.runner = runnerFunc(func(ctx context.Context, _ domain.Target, _ []string, _ string) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		})
		if r := s.Measure(context.Background(), directory("/fixture"), false); r.Status != "timeout" {
			t.Fatalf("wrong timeout status: %+v", r)
		}
	})
	t.Run("cancel-before-start", func(t *testing.T) {
		s := testService(t)
		s.runner = runnerFunc(func(context.Context, domain.Target, []string, string) ([]byte, error) {
			t.Fatal("canceled work launched command")
			return nil, nil
		})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if r := s.Measure(ctx, directory("/fixture"), false); r.Status != "canceled" {
			t.Fatalf("wrong canceled status: %+v", r)
		}
	})
	t.Run("cancel-queued", func(t *testing.T) {
		s := testService(t)
		s.slots <- struct{}{}
		s.slots <- struct{}{}
		s.runner = runnerFunc(func(context.Context, domain.Target, []string, string) ([]byte, error) {
			t.Fatal("queued canceled measurement launched")
			return nil, nil
		})
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if r := s.Measure(ctx, directory("/fixture"), false); r.Status != "timeout" {
			t.Fatalf("queued timeout: %+v", r)
		}
	})
}

func TestOldInFlightCompletionCannotRepopulateForcedCache(t *testing.T) {
	s := testService(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	s.runner = runnerFunc(func(context.Context, domain.Target, []string, string) ([]byte, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return []byte("1\t.\n"), nil
		}
		return []byte("2\t.\n"), errors.New("refresh failed")
	})
	item := directory("/fixture")
	done := make(chan Result, 1)
	go func() { done <- s.Measure(context.Background(), item, false) }()
	<-started
	if r := s.Measure(context.Background(), item, true); r.Status != "partial" {
		t.Fatalf("expected partial refresh: %+v", r)
	}
	close(release)
	<-done
	if r := s.Measure(context.Background(), item, false); r.Cached || r.Status != "partial" {
		t.Fatalf("old completed measurement was restored after forced refresh: %+v", r)
	}
}

func TestRealBSDAndGNUDUIncludeHiddenIgnoredWithoutFollowingLinks(t *testing.T) {
	for _, tool := range []string{"du", "gdu"} {
		t.Run(tool, func(t *testing.T) {
			executable, err := exec.LookPath(tool)
			if err != nil {
				t.Skip(tool + " unavailable")
			}
			root := filepath.Join(t.TempDir(), "folder with 'quote'\nand newline")
			if err = os.MkdirAll(filepath.Join(root, "ignored"), 0700); err != nil {
				t.Fatal(err)
			}
			write := func(path string, n int) {
				t.Helper()
				if err := os.WriteFile(path, bytes.Repeat([]byte{0x5a}, n), 0600); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(root, ".hidden"), 128*1024)
			write(filepath.Join(root, "ignored", "ignored.bin"), 256*1024)
			if err = os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored/\n"), 0600); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			write(filepath.Join(outside, "outside.bin"), 2*1024*1024)
			if err = os.Symlink(outside, filepath.Join(root, "outside-link")); err != nil {
				t.Fatal(err)
			}
			s := testService(t)
			runner := transport.New(config.Defaults())
			s.runner = runnerFunc(func(ctx context.Context, target domain.Target, argv []string, cwd string) ([]byte, error) {
				argv = append([]string(nil), argv...)
				argv[2] = executable
				return runner.Run(ctx, target, argv, cwd)
			})
			result := s.Measure(context.Background(), directory(root), false)
			if result.Status != "complete" || result.Bytes < 384*1024 {
				t.Fatalf("hidden/ignored content missing: %+v", result)
			}
			physical := directDU(t, executable, root, "-skP")
			followed := directDU(t, executable, root, "-skL")
			if result.Bytes != physical || result.Bytes >= followed {
				t.Fatalf("symlink handling: measured=%d physical=%d followed=%d", result.Bytes, physical, followed)
			}
		})
	}
}

func directDU(t *testing.T, tool, path, flags string) int64 {
	t.Helper()
	command := exec.Command(tool, flags, ".")
	command.Dir = path
	out, err := command.Output()
	if err != nil {
		t.Fatalf("reference du: %v", err)
	}
	fields := strings.Fields(string(out))
	n, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n * 1024
}

func TestFakeSSHMeasuresQuotedDirectoryWithoutNetwork(t *testing.T) {
	if _, err := exec.LookPath("du"); err != nil {
		t.Skip("du unavailable")
	}
	dir := t.TempDir()
	root := filepath.Join(dir, "root 'quoted'\nnew line")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "data"), bytes.Repeat([]byte{1}, 8192), 0600); err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(dir, "captured-command")
	fakeSSH := filepath.Join(dir, "ssh")
	script := "#!/bin/sh\nfor argument do command=$argument; done\nprintf '%s' \"$command\" > " + transport.Quote(capture) + "\nexec /bin/sh -c \"$command\"\n"
	if err := os.WriteFile(fakeSSH, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Tools.SSH = fakeSSH
	s := New(cfg)
	item := directory(root)
	item.Target = domain.Target{Host: "fixture-not-a-real-host"}
	item.ID = domain.ItemID(item.Target, root)
	r := s.Measure(context.Background(), item, false)
	if r.Status != "complete" || r.Bytes < 8192 {
		t.Fatalf("fake SSH measurement failed: %+v", r)
	}
	got, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	want := transport.ShellCommand([]string{"env", "LC_ALL=C", "du", "-skP", "."}, root)
	if string(got) != want {
		t.Fatalf("wrong remote command: %q, want %q", got, want)
	}
}

func TestRealTransportCancellationStopsSleepingFakeSSH(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	fakeSSH := filepath.Join(dir, "ssh")
	script := "#!/bin/sh\nprintf ready > " + transport.Quote(ready) + "\nexec sleep 30\n"
	if err := os.WriteFile(fakeSSH, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Tools.SSH = fakeSSH
	s := New(cfg)
	item := directory(dir)
	item.Target = domain.Target{Host: "fixture"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan Result, 1)
	go func() { done <- s.Measure(ctx, item, false) }()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake SSH did not start")
		}
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	select {
	case result := <-done:
		if result.Status != "canceled" {
			t.Fatalf("unexpected result: %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not stop transport promptly")
	}
}
