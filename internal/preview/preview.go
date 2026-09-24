// Package preview renders bounded, cancelable previews and disposable cached text.
package preview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/daviddwlee84/lazyfind/internal/actions"
	"github.com/daviddwlee84/lazyfind/internal/cache"
	"github.com/daviddwlee84/lazyfind/internal/config"
	"github.com/daviddwlee84/lazyfind/internal/domain"
	"github.com/daviddwlee84/lazyfind/internal/search"
	"github.com/daviddwlee84/lazyfind/internal/transport"
)

const MaxBytes = 64 << 10

var errLimit = errors.New("preview output limit")

type Service struct {
	actions *actions.Service
	cache   *cache.Store
	runner  *transport.Runner
	search  *search.Service
	mu      sync.Mutex
	epoch   map[string]uint64
}

func New(cfg config.Config) *Service {
	return &Service{actions: actions.New(cfg), cache: cache.New(cfg), runner: transport.New(cfg), search: search.New(cfg), epoch: map[string]uint64{}}
}

// Invalidate forces a new preview after returning from an editor, including on
// filesystems whose modification timestamps have only second precision.
func (s *Service) Invalidate(item domain.Item) { s.mu.Lock(); s.epoch[item.ID]++; s.mu.Unlock() }

// Load rechecks metadata before consulting the cache. Historical offline views
// should display their stored snippets directly instead of calling Load.
func (s *Service) Load(ctx context.Context, item domain.Item, queryText string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	current, err := s.search.Inspect(ctx, item.Target, item.RawPath())
	if err != nil {
		return "", err
	}
	item.Size = current.Size
	item.Modified = current.Modified
	item.Kind = current.Kind
	resolved, err := s.actions.Resolve(ctx, item, queryText)
	if err != nil {
		return "", err
	}
	return s.load(ctx, item, queryText, resolved)
}

// LoadResolved can reuse classification already produced by an async action
// effect. It still rechecks metadata so cached previews cannot mask local edits.
func (s *Service) LoadResolved(ctx context.Context, item domain.Item, queryText string, resolved actions.Resolved) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	current, err := s.search.Inspect(ctx, item.Target, item.RawPath())
	if err != nil {
		return "", err
	}
	item.Size = current.Size
	item.Modified = current.Modified
	item.Kind = current.Kind
	return s.load(ctx, item, queryText, resolved)
}

func (s *Service) load(ctx context.Context, item domain.Item, queryText string, resolved actions.Resolved) (string, error) {
	s.mu.Lock()
	epoch := s.epoch[item.ID]
	s.mu.Unlock()
	keyData, _ := json.Marshal(struct {
		Version  int
		Item     domain.Item
		Query    string
		Resolved actions.Resolved
		Epoch    uint64
	}{1, item, queryText, resolved, epoch})
	key := string(keyData)
	cacheable := item.Kind != "symlink" && item.Kind != "other"
	if cacheable {
		if text, ok := s.cache.Get(key); ok {
			return text, nil
		}
	}
	var body string
	var err error
	if resolved.Preview != "" {
		a, ok := resolved.Find(resolved.Preview)
		if !ok {
			return "", fmt.Errorf("preview action %q is not configured", resolved.Preview)
		}
		if !a.Available {
			return "", fmt.Errorf("preview: %s", a.Reason)
		}
		if a.Mode != "preview" {
			return "", fmt.Errorf("action %q is not a preview action", a.ID)
		}
		argv, e := actions.Expand(a.Argv, item, queryText, resolved.GitRoot)
		if e != nil {
			return "", e
		}
		cwd, e := actions.Expand([]string{a.Cwd}, item, queryText, resolved.GitRoot)
		if e != nil {
			return "", e
		}
		body, err = s.command(ctx, item.Target, argv, cwd[0])
	} else if item.Kind == "directory" || item.Kind == "dir" {
		body, err = directory(item)
	} else if hasExtracted(item) {
		body = "Document matches (extracted text):\n" + snippets(item, true)
	} else if actions.IsTextMIME(resolved.MIME) {
		if item.Target.Remote() {
			body, err = s.command(ctx, item.Target, []string{"head", "-c", fmt.Sprint(MaxBytes + 1), item.RawPath()}, "")
		} else {
			body, err = readText(item.RawPath())
		}
	} else {
		body = "No text preview for this file type. Configure a preview rule to use another tool.\n"
		if len(item.Matches) > 0 {
			body += "\nSearch matches:\n" + snippets(item, false)
		}
	}
	if err != nil {
		return "", err
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	text := metadata(item, resolved) + "\n" + clean(body)
	if len(text) > MaxBytes {
		text = truncate(text, MaxBytes) + "\n… preview truncated"
	}
	// Cache failure is disposable and must not discard a successfully read preview.
	if cacheable {
		_ = s.cache.Put(key, text)
	}
	return text, nil
}

func (s *Service) command(ctx context.Context, target domain.Target, argv []string, cwd string) (string, error) {
	var buf bytes.Buffer
	err := s.runner.Stream(ctx, target, argv, cwd, func(r io.Reader) error {
		_, e := io.Copy(&buf, io.LimitReader(r, MaxBytes+1))
		if e == nil && buf.Len() > MaxBytes {
			return errLimit
		}
		return e
	})
	if errors.Is(err, errLimit) {
		return string(buf.Bytes()[:MaxBytes]) + "\n… preview truncated", nil
	}
	return buf.String(), err
}
func readText(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "No text preview for this special file.", nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil {
		return "", err
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "Binary data: text preview suppressed.", nil
	}
	if len(data) > MaxBytes {
		return string(data[:MaxBytes]) + "\n… preview truncated", nil
	}
	return string(data), nil
}
func directory(item domain.Item) (string, error) {
	if item.Target.Remote() {
		return "Remote directory. Open Yazi or a shell to browse its contents.", nil
	}
	f, err := os.Open(item.RawPath())
	if err != nil {
		return "", err
	}
	defer f.Close()
	entries, err := f.ReadDir(201)
	if err != nil && err != io.EOF {
		return "", err
	}
	truncated := len(entries) > 200
	if truncated {
		entries = entries[:200]
	}
	sort.Slice(entries, func(i, j int) bool { return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name()) })
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(domain.Display(e.Name()))
		if e.IsDir() {
			b.WriteByte('/')
		}
		b.WriteByte('\n')
	}
	if len(entries) == 0 {
		b.WriteString("Empty directory.\n")
	}
	if truncated {
		b.WriteString("… directory listing truncated\n")
	}
	return b.String(), nil
}
func metadata(item domain.Item, r actions.Resolved) string {
	modified := "unknown"
	if item.Modified != nil {
		modified = item.Modified.Local().Format(time.RFC3339)
	}
	text := fmt.Sprintf("%s\n%s · %s · %s\nModified: %s\n", domain.Display(item.RawPath()), item.Kind, domain.HumanSize(item.Size), r.MIME, modified)
	if item.Target.Remote() {
		text += "Host: " + domain.Display(item.Target.Host) + "\n"
	}
	if r.GitRoot != "" {
		text += "Git: " + domain.Display(r.GitRoot) + "\n"
	}
	if len(item.Matches) > 0 && !hasExtracted(item) {
		text += "\nSearch matches:\n" + snippets(item, false)
	}
	return text
}
func hasExtracted(item domain.Item) bool {
	for _, m := range item.Matches {
		if m.Line > 0 || m.Extracted {
			return m.Extracted
		}
	}
	return false
}
func snippets(item domain.Item, extractedOnly bool) string {
	var b strings.Builder
	n := 0
	for _, m := range item.Matches {
		if extractedOnly && !m.Extracted {
			continue
		}
		if m.Text == "" {
			continue
		}
		n++
		label := m.Source
		if m.Extracted {
			label += " extracted line"
		} else {
			label += " line"
		}
		text := strings.TrimSuffix(m.Text, "\n")
		if len(text) > 1024 {
			text = text[:1024] + "…"
		}
		fmt.Fprintf(&b, "%s %d: %s\n", label, m.Line, clean(text))
		if n == 8 {
			break
		}
	}
	return b.String()
}
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return '�'
		}
		return r
	}, strings.ToValidUTF8(s, "�"))
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit]
}
