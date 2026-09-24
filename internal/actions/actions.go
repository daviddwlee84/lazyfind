// Package actions resolves downstream tools from file facts and ordered user rules.
// Commands retain argument boundaries; only transport owns the SSH shell boundary.
package actions

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/daviddwlee84/lazyfind/internal/config"
	"github.com/daviddwlee84/lazyfind/internal/domain"
	"github.com/daviddwlee84/lazyfind/internal/transport"
)

var ErrBuiltinCopy = errors.New("copy is a built-in action; use CopyPath")

type ResolvedAction struct {
	config.Action
	Available bool
	Reason    string
}
type Resolved struct {
	Actions []ResolvedAction
	Default string
	Preview string
	MIME    string
	GitRoot string
}

func (r Resolved) Find(id string) (ResolvedAction, bool) {
	for _, a := range r.Actions {
		if a.ID == id {
			return a, true
		}
	}
	return ResolvedAction{}, false
}

type availability struct {
	available bool
	expires   time.Time
}
type Service struct {
	cfg    config.Config
	runner *transport.Runner
	mu     sync.Mutex
	tools  map[string]availability
}

func New(cfg config.Config) *Service {
	return &Service{cfg: cfg, runner: transport.New(cfg), tools: map[string]availability{}}
}

// Resolve performs filesystem and tool probes. Call it in a cancelable UI effect.
func (s *Service) Resolve(ctx context.Context, item domain.Item, queryText string) (Resolved, error) {
	if err := ctx.Err(); err != nil {
		return Resolved{}, err
	}
	r := Resolved{MIME: s.mime(ctx, item), GitRoot: s.gitRoot(ctx, item)}
	definitions := s.builtins(item)
	for _, a := range s.cfg.Actions {
		definitions[a.ID] = a
	}
	ids := []string{}
	seen := map[string]bool{}
	add := func(id string) {
		if id != "" && !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	// User rules add actions and select defaults before builtin fallback rules.
	for _, rule := range s.cfg.Rules {
		if !matches(rule, item, r) {
			continue
		}
		for _, id := range rule.Actions {
			add(id)
		}
		if r.Default == "" && rule.Default != "" {
			r.Default = rule.Default
			add(rule.Default)
		}
		if r.Preview == "" && rule.Preview != "" {
			r.Preview = rule.Preview
			add(rule.Preview)
		}
	}
	add("editor")
	add("yazi")
	if r.GitRoot != "" {
		add("lazygit")
	}
	if item.Kind != "directory" && item.Kind != "dir" {
		add("bat")
		if strings.EqualFold(filepath.Ext(item.RawPath()), ".md") || strings.EqualFold(filepath.Ext(item.RawPath()), ".markdown") {
			add("glow")
		}
	}
	if !item.Target.Remote() {
		add("open")
	}
	add("copy")
	add("shell")
	for _, id := range ids {
		a, ok := definitions[id]
		if !ok {
			r.Actions = append(r.Actions, ResolvedAction{Action: config.Action{ID: id, Label: id}, Reason: "action is not configured"})
			continue
		}
		if a.Label == "" {
			a.Label = a.ID
		}
		if a.Mode == "" {
			a.Mode = "suspend"
		}
		ra := ResolvedAction{Action: a, Available: true}
		switch {
		case a.Location == "local" && item.Target.Remote():
			ra.Available = false
			ra.Reason = "local action cannot use a remote path"
		case a.Location == "remote" && !item.Target.Remote():
			ra.Available = false
			ra.Reason = "action requires a remote target"
		case id == "copy" && len(a.Argv) == 0: // handled by the UI clipboard transport
		case len(a.Argv) == 0:
			ra.Available = false
			ra.Reason = "action has no command"
		default:
			argv, err := Expand(a.Argv, item, queryText, r.GitRoot)
			if err == nil {
				_, err = Expand([]string{a.Cwd}, item, queryText, r.GitRoot)
			}
			if err != nil {
				ra.Available = false
				ra.Reason = err.Error()
			} else if !s.available(ctx, item.Target, argv[0]) {
				ra.Available = false
				ra.Reason = "tool unavailable: " + argv[0]
			}
		}
		r.Actions = append(r.Actions, ra)
	}
	if err := ctx.Err(); err != nil {
		return r, err
	}
	usable := func(id string) bool { a, ok := r.Find(id); return ok && a.Available }
	if r.Default == "" {
		switch {
		case isDirectory(item) && r.GitRoot != "" && samePath(item.RawPath(), r.GitRoot, item.Target.Remote()) && usable("lazygit"):
			r.Default = "lazygit"
		case isDirectory(item) && usable("yazi"):
			r.Default = "yazi"
		case isDirectory(item):
			r.Default = "shell"
		case IsTextMIME(r.MIME) && usable("editor"):
			r.Default = "editor"
		case !item.Target.Remote() && usable("open"):
			r.Default = "open"
		default:
			r.Default = "editor"
		}
	}
	selectedExtracted := len(item.Matches) > 0 && item.Matches[0].Extracted
	if r.Preview == "" && item.Kind == "file" && IsTextMIME(r.MIME) && !selectedExtracted && usable("bat") {
		r.Preview = "bat"
	}
	return r, nil
}

func (s *Service) builtins(item domain.Item) map[string]config.Action {
	editor := []string{"vi"}
	if !item.Target.Remote() {
		value := os.Getenv("VISUAL")
		if value == "" {
			value = os.Getenv("EDITOR")
		}
		if words, err := SplitCommand(value); err == nil && len(words) > 0 {
			editor = words
		}
	}
	line := nativeLine(item)
	if line > 0 {
		switch filepath.Base(editor[0]) {
		case "vi", "vim", "nvim", "nano", "emacs":
			editor = append(editor, "+"+strconv.Itoa(line))
		}
	}
	editor = append(editor, "{path}")
	lineRange := ":300"
	if line > 0 {
		lineRange = fmt.Sprintf("%d:%d", max(1, line-40), line+100)
	}
	shell := "sh"
	if !item.Target.Remote() && os.Getenv("SHELL") != "" {
		shell = os.Getenv("SHELL")
	}
	opener := []string{"xdg-open", "{path}"}
	if runtime.GOOS == "darwin" {
		opener = []string{"open", "--", "{path}"}
	}
	return map[string]config.Action{
		"editor":  {ID: "editor", Label: "Open in editor", Argv: editor, Cwd: "{dir}", Mode: "suspend"},
		"yazi":    {ID: "yazi", Label: "Reveal in Yazi", Argv: []string{"yazi", "{path}"}, Cwd: "{dir}", Mode: "suspend"},
		"lazygit": {ID: "lazygit", Label: "Open Lazygit", Argv: []string{"lazygit", "-p", "{git_root}"}, Cwd: "{git_root}", Mode: "suspend"},
		"glow":    {ID: "glow", Label: "Read Markdown", Argv: []string{"glow", "-p", "{path}"}, Cwd: "{dir}", Mode: "suspend"},
		"bat":     {ID: "bat", Label: "Preview with bat", Argv: []string{"bat", "--color=never", "--style=numbers", "--paging=never", "--line-range=" + lineRange, "--", "{path}"}, Cwd: "{dir}", Mode: "preview"},
		"open":    {ID: "open", Label: "Open with system app", Argv: opener, Cwd: "{dir}", Mode: "detach", Location: "local"},
		"copy":    {ID: "copy", Label: "Copy path", Mode: "internal"},
		"shell":   {ID: "shell", Label: "Open shell here", Argv: []string{shell}, Cwd: "{dir}", Mode: "suspend"},
	}
}

func (s *Service) available(ctx context.Context, target domain.Target, tool string) bool {
	key := target.ID() + "\x00" + tool
	s.mu.Lock()
	cached, ok := s.tools[key]
	s.mu.Unlock()
	if ok && time.Now().Before(cached.expires) {
		return cached.available
	}
	found := s.runner.Available(ctx, target, tool)
	if ctx.Err() == nil {
		s.mu.Lock()
		s.tools[key] = availability{found, time.Now().Add(30 * time.Second)}
		s.mu.Unlock()
	}
	return found
}

func (s *Service) mime(ctx context.Context, item domain.Item) string {
	if isDirectory(item) {
		return "inode/directory"
	}
	known := mime.TypeByExtension(strings.ToLower(filepath.Ext(item.RawPath())))
	if known != "" {
		return strings.SplitN(known, ";", 2)[0]
	}
	if item.Target.Remote() {
		out, err := s.runner.Run(ctx, item.Target, []string{"file", "--brief", "--mime-type", "--", item.RawPath()}, "")
		if err == nil {
			return strings.TrimSpace(string(out))
		}
		return "application/octet-stream"
	}
	info, err := os.Stat(item.RawPath())
	if err != nil || !info.Mode().IsRegular() {
		return "application/octet-stream"
	}
	f, err := os.Open(item.RawPath())
	if err != nil {
		return "application/octet-stream"
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return "application/octet-stream"
	}
	return strings.SplitN(http.DetectContentType(buf[:n]), ";", 2)[0]
}

func (s *Service) gitRoot(ctx context.Context, item domain.Item) string {
	dir := itemDir(item)
	out, err := s.runner.Run(ctx, item.Target, []string{"git", "-C", dir, "rev-parse", "--show-toplevel"}, "")
	if err == nil {
		return strings.TrimSuffix(string(out), "\n")
	}
	// Worktrees use --show-toplevel; bare repositories have no working tree.
	out, err = s.runner.Run(ctx, item.Target, []string{"git", "-C", dir, "rev-parse", "--is-bare-repository", "--absolute-git-dir"}, "")
	if err == nil && strings.HasPrefix(string(out), "true\n") {
		return strings.TrimSuffix(strings.TrimPrefix(string(out), "true\n"), "\n")
	}
	return ""
}
func matches(rule config.Rule, item domain.Item, r Resolved) bool {
	if rule.Location == "local" && item.Target.Remote() || rule.Location == "remote" && !item.Target.Remote() {
		return false
	}
	if rule.Git && r.GitRoot == "" {
		return false
	}
	if len(rule.Kinds) > 0 {
		ok := false
		for _, k := range rule.Kinds {
			if k == item.Kind || (k == "dir" || k == "directory") && isDirectory(item) {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	if len(rule.Extensions) > 0 {
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(item.RawPath())), ".")
		ok := false
		for _, e := range rule.Extensions {
			if strings.EqualFold(strings.TrimPrefix(e, "."), ext) {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	if rule.MIME != "" {
		ok, err := filepath.Match(rule.MIME, r.MIME)
		if err != nil || !ok {
			return false
		}
	}
	return true
}
func isDirectory(item domain.Item) bool { return item.Kind == "directory" || item.Kind == "dir" }

// IsTextMIME includes common source and structured text types whose registered
// MIME names start with application/ rather than text/.
func IsTextMIME(m string) bool {
	if strings.HasPrefix(m, "text/") || strings.HasSuffix(m, "+json") || strings.HasSuffix(m, "+xml") {
		return true
	}
	switch m {
	case "application/json", "application/xml", "application/javascript", "application/x-javascript", "application/sql", "application/yaml", "application/x-yaml", "application/toml", "application/x-sh", "application/x-shellscript", "application/x-httpd-php", "application/x-perl", "application/x-python", "application/x-empty":
		return true
	}
	return false
}
func samePath(a, b string, remote bool) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	if remote {
		return false
	}
	canonicalA, errA := filepath.EvalSymlinks(a)
	canonicalB, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && canonicalA == canonicalB
}
func itemDir(item domain.Item) string {
	if isDirectory(item) {
		return item.RawPath()
	}
	return filepath.Dir(item.RawPath())
}

// Expand expands typed placeholders within a single argument, never into shell text.
func Expand(argv []string, item domain.Item, queryText, gitRoot string) ([]string, error) {
	line := max(1, nativeLine(item))
	values := map[string]string{"path": item.RawPath(), "dir": itemDir(item), "name": filepath.Base(item.RawPath()), "ext": strings.TrimPrefix(filepath.Ext(item.RawPath()), "."), "git_root": gitRoot, "query": queryText, "line": strconv.Itoa(line)}
	result := make([]string, len(argv))
	for i, arg := range argv {
		var b strings.Builder
		for {
			start := strings.IndexByte(arg, '{')
			if start < 0 {
				b.WriteString(arg)
				break
			}
			prefix := arg[:start]
			b.WriteString(prefix)
			arg = arg[start+1:]
			end := strings.IndexByte(arg, '}')
			if end < 0 {
				b.WriteByte('{')
				b.WriteString(arg)
				break
			}
			key := arg[:end]
			// Explicit shell scripts, JSON and awk programs may use braces of
			// their own. Only plain named tokens are action placeholders, and
			// shell ${variables} remain the shell's responsibility.
			if strings.HasSuffix(prefix, "$") {
				b.WriteByte('{')
				b.WriteString(arg[:end+1])
				arg = arg[end+1:]
				continue
			}
			plain := key != ""
			for _, r := range key {
				if !unicode.IsLetter(r) && r != '_' {
					plain = false
					break
				}
			}
			if !plain {
				b.WriteByte('{')
				continue
			}
			value, ok := values[key]
			if !ok {
				return nil, fmt.Errorf("unknown action placeholder {%s}", key)
			}
			if key == "git_root" && value == "" {
				return nil, errors.New("action requires a Git repository")
			}
			b.WriteString(value)
			arg = arg[end+1:]
		}
		result[i] = b.String()
	}
	return result, nil
}

// A selected match is placed first by the UI. Extracted document locations
// never become source-file line numbers, even when a later match is native.
func nativeLine(item domain.Item) int {
	if len(item.Matches) > 0 && item.Matches[0].Extracted {
		return 0
	}
	for _, m := range item.Matches {
		if !m.Extracted && m.Line > 0 {
			return m.Line
		}
	}
	return 0
}

// Prepare rechecks a possibly stale history item before preparing a handoff.
// The caller owns terminal release, execution, and reacquisition.
func (s *Service) Prepare(ctx context.Context, item domain.Item, queryText, id string) (*exec.Cmd, error) {
	probe, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if err := s.Exists(probe, item); err != nil {
		return nil, err
	}
	r, err := s.Resolve(probe, item, queryText)
	if err != nil {
		return nil, err
	}
	a, ok := r.Find(id)
	if !ok {
		return nil, fmt.Errorf("action %q does not apply to this item", id)
	}
	if !a.Available {
		return nil, errors.New(a.Reason)
	}
	if id == "copy" && len(a.Argv) == 0 {
		return nil, ErrBuiltinCopy
	}
	argv, err := Expand(a.Argv, item, queryText, r.GitRoot)
	if err != nil {
		return nil, err
	}
	cwd, err := Expand([]string{a.Cwd}, item, queryText, r.GitRoot)
	if err != nil {
		return nil, err
	}
	if a.Mode == "suspend" || a.Mode == "replace" {
		return s.runner.TerminalCommand(ctx, item.Target, argv, cwd[0])
	}
	return s.runner.Command(ctx, item.Target, argv, cwd[0])
}
func (s *Service) ExecCmd(ctx context.Context, item domain.Item, queryText, id string) (*exec.Cmd, error) {
	return s.Prepare(ctx, item, queryText, id)
}
func (s *Service) Exists(ctx context.Context, item domain.Item) error {
	if !item.Target.Remote() {
		_, err := os.Stat(item.RawPath())
		if err != nil {
			return fmt.Errorf("item is no longer available: %w", err)
		}
		return nil
	}
	_, err := s.runner.Run(ctx, item.Target, []string{"test", "-e", item.RawPath()}, "")
	if err != nil {
		return fmt.Errorf("remote item is no longer available: %w", err)
	}
	return nil
}
func CopyPath(item domain.Item) string {
	if item.Target.Remote() {
		return item.Target.Host + ":" + item.RawPath()
	}
	return item.RawPath()
}

// SplitCommand accepts shell-style quotes in VISUAL/EDITOR but performs no shell
// expansion, substitutions, redirects or command execution.
func SplitCommand(s string) ([]string, error) {
	var words []string
	var b strings.Builder
	var quote rune
	escaped, started := false, false
	for _, r := range s {
		if escaped {
			b.WriteRune(r)
			escaped = false
			started = true
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			started = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				b.WriteRune(r)
			}
			started = true
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			started = true
			continue
		}
		if unicode.IsSpace(r) {
			if started {
				words = append(words, b.String())
				b.Reset()
				started = false
			}
			continue
		}
		b.WriteRune(r)
		started = true
	}
	if quote != 0 || escaped {
		return nil, errors.New("unterminated quote or escape in command")
	}
	if started {
		words = append(words, b.String())
	}
	return words, nil
}
