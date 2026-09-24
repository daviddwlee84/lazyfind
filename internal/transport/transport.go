// Package transport runs typed commands locally or through the user's OpenSSH.
// Only the SSH boundary is a shell boundary; each argument is quoted separately.
package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/daviddwlee84/lazyfind/internal/config"
	"github.com/daviddwlee84/lazyfind/internal/domain"
)

type Runner struct{ cfg config.Config }

func New(cfg config.Config) *Runner { return &Runner{cfg: cfg} }

// Quote quotes one argument for a POSIX shell, including empty strings and newlines.
func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func ShellCommand(argv []string, cwd string) string {
	parts := make([]string, len(argv))
	for i, s := range argv {
		parts[i] = Quote(s)
	}
	command := "exec " + strings.Join(parts, " ")
	if cwd != "" {
		command = "cd -- " + Quote(cwd) + " && " + command
	}
	return command
}
func validate(target domain.Target, argv []string, cwd string) error {
	if len(argv) == 0 || argv[0] == "" {
		return errors.New("empty executable")
	}
	for _, s := range append(append([]string{}, argv...), cwd) {
		if strings.ContainsRune(s, 0) {
			return errors.New("command argument contains NUL")
		}
	}
	if target.Remote() && (strings.HasPrefix(target.Host, "-") || strings.IndexFunc(target.Host, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0) {
		return errors.New("SSH alias must not start with '-' or contain whitespace or controls")
	}
	return nil
}

// Command creates a command without taking terminal ownership. Interactive callers
// may replace SSH's BatchMode option explicitly when handing off to native SSH.
func (r *Runner) Command(ctx context.Context, target domain.Target, argv []string, cwd string) (*exec.Cmd, error) {
	if err := validate(target, argv, cwd); err != nil {
		return nil, err
	}
	var cmd *exec.Cmd
	if target.Remote() {
		ssh := r.cfg.Tools.SSH
		if ssh == "" {
			ssh = "ssh"
		}
		cmd = exec.CommandContext(ctx, ssh, "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "--", target.Host, ShellCommand(argv, cwd))
	} else {
		cmd = exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir = cwd
	}
	return cmd, nil
}

type CommandError struct {
	Err    error
	Code   int
	Stderr string
}

func (e *CommandError) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("%v: %s", e.Err, strings.TrimSpace(e.Stderr))
	}
	return e.Err.Error()
}
func (e *CommandError) Unwrap() error { return e.Err }
func ExitCode(err error) int {
	var e *CommandError
	if errors.As(err, &e) {
		return e.Code
	}
	var x *exec.ExitError
	if errors.As(err, &x) {
		return x.ExitCode()
	}
	return -1
}

type limitedBuffer struct {
	b         bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.b.Len()
	if remaining > 0 {
		keep := len(p)
		if keep > remaining {
			keep = remaining
		}
		b.b.Write(p[:keep])
	}
	if n > remaining {
		b.truncated = true
	}
	return n, nil
}

// Stream consumes stdout while the process is running. The consumer must consume
// through EOF or return an error; an early return cancels the child promptly.
func (r *Runner) Stream(ctx context.Context, target domain.Target, argv []string, cwd string, consume func(io.Reader) error) error {
	cmd, err := r.Command(ctx, target, argv, cwd)
	if err != nil {
		return err
	}
	configureBackground(cmd)
	stderr := &limitedBuffer{limit: 16 << 10}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	readErr := consume(stdout)
	if readErr != nil {
		_ = cmd.Cancel()
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if readErr != nil {
		return readErr
	}
	if waitErr != nil {
		code := -1
		var e *exec.ExitError
		if errors.As(waitErr, &e) {
			code = e.ExitCode()
		}
		return &CommandError{Err: waitErr, Code: code, Stderr: stderr.b.String()}
	}
	return nil
}
func (r *Runner) Run(ctx context.Context, target domain.Target, argv []string, cwd string) ([]byte, error) {
	var out bytes.Buffer
	err := r.Stream(ctx, target, argv, cwd, func(rd io.Reader) error {
		_, e := io.Copy(&out, io.LimitReader(rd, 8<<20+1))
		if e == nil && out.Len() > 8<<20 {
			return errors.New("command output exceeds 8 MiB")
		}
		return e
	})
	return out.Bytes(), err
}
func (r *Runner) Available(ctx context.Context, target domain.Target, tool string) bool {
	if tool == "" {
		return false
	}
	if !target.Remote() {
		_, err := exec.LookPath(tool)
		return err == nil
	}
	probe, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := r.Run(probe, target, []string{"sh", "-c", "command -v \"$1\" >/dev/null 2>&1", "lazyfind", tool}, "")
	return err == nil
}

// ResolveRoots resolves ~ and relative paths on the target, never on the client
// for an SSH target. Resolution also rejects nonexistent or non-directory roots.
func (r *Runner) ResolveRoots(ctx context.Context, target domain.Target, roots []string) ([]string, error) {
	if len(roots) == 0 {
		roots = []string{"."}
	}
	result := make([]string, 0, len(roots))
	seen := map[string]bool{}
	if !target.Remote() {
		for _, root := range roots {
			if root == "~" || strings.HasPrefix(root, "~/") {
				home, err := os.UserHomeDir()
				if err != nil {
					return nil, err
				}
				if root == "~" {
					root = home
				} else {
					root = filepath.Join(home, root[2:])
				}
			}
			abs, err := filepath.Abs(root)
			if err != nil {
				return nil, err
			}
			fi, err := os.Stat(abs)
			if err != nil {
				return nil, err
			}
			if !fi.IsDir() {
				return nil, fmt.Errorf("root is not a directory: %s", domain.Display(abs))
			}
			// fd emits physical absolute paths; use that identity for roots too.
			abs, err = filepath.EvalSymlinks(abs)
			if err != nil {
				return nil, err
			}
			if !seen[abs] {
				result = append(result, abs)
				seen[abs] = true
			}
		}
		return result, nil
	}
	script := `for root do
 case "$root" in '~') root=$HOME;; '~/'*) root=$HOME/${root#\~/};; esac
 (CDPATH= cd -P -- "$root" && printf '%s\000' "$PWD") || exit
 done`
	argv := append([]string{"sh", "-c", script, "lazyfind-roots"}, roots...)
	out, err := r.Run(ctx, target, argv, "")
	if err != nil {
		return nil, fmt.Errorf("resolve remote roots: %w", err)
	}
	for _, p := range bytes.Split(out, []byte{0}) {
		if len(p) == 0 {
			continue
		}
		root := string(p)
		if !seen[root] {
			result = append(result, root)
			seen[root] = true
		}
	}
	if len(result) == 0 {
		return nil, errors.New("remote returned no roots")
	}
	return result, nil
}

// TerminalCommand permits native SSH authentication and allocates a remote TTY.
// It is intended only for explicit terminal handoffs, never background effects.
func (r *Runner) TerminalCommand(ctx context.Context, target domain.Target, argv []string, cwd string) (*exec.Cmd, error) {
	cmd, err := r.Command(ctx, target, argv, cwd)
	if err != nil {
		return nil, err
	}
	if target.Remote() {
		ssh := r.cfg.Tools.SSH
		if ssh == "" {
			ssh = "ssh"
		}
		cmd = exec.CommandContext(ctx, ssh, "-t", "-o", "ConnectTimeout=10", "--", target.Host, ShellCommand(argv, cwd))
	}
	return cmd, nil
}
