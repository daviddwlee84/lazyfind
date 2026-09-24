// Package preview loads bounded, cancelable structured previews. Display styling
// belongs to the TUI; cached documents retain source line numbers and byte spans.
package preview

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
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

type Line struct {
	Number int           `json:"number"`
	Text   string        `json:"text"`
	Spans  []domain.Span `json:"spans,omitempty"`
}
type Document struct {
	Metadata  string         `json:"metadata"`
	Lines     []Line         `json:"lines,omitempty"`
	Snippets  []domain.Match `json:"snippets,omitempty"`
	Opaque    string         `json:"opaque,omitempty"`
	Changed   bool           `json:"changed,omitempty"`
	Truncated bool           `json:"truncated,omitempty"`
}

type Service struct {
	actions *actions.Service
	cache   *cache.Store
	runner  *transport.Runner
	search  *search.Service
	custom  map[string]bool
	mu      sync.Mutex
	epoch   map[string]uint64
}

func New(cfg config.Config) *Service {
	custom := make(map[string]bool)
	for _, a := range cfg.Actions {
		custom[a.ID] = true
	}
	return &Service{actions: actions.New(cfg), cache: cache.New(cfg), runner: transport.New(cfg), search: search.New(cfg), custom: custom, epoch: map[string]uint64{}}
}

// Invalidate forces a reload after editor handoffs, even on coarse filesystems.
func (s *Service) Invalidate(item domain.Item) { s.mu.Lock(); s.epoch[item.ID]++; s.mu.Unlock() }

// Load and LoadResolved preserve the plain-text API for non-TUI callers.
// Opening historical results must use their stored snippets, not these methods.
func (s *Service) Load(ctx context.Context, item domain.Item, query string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resolved, err := s.actions.Resolve(ctx, item, query)
	if err != nil {
		return "", err
	}
	return s.LoadResolved(ctx, item, query, resolved)
}
func (s *Service) LoadResolved(ctx context.Context, item domain.Item, query string, resolved actions.Resolved) (string, error) {
	document, err := s.LoadDocument(ctx, item, query, resolved)
	return document.Text(false), err
}

// LoadDocument rechecks metadata before cache lookup. Original search metadata
// must remain available: native spans are only valid when it still matches.
func (s *Service) LoadDocument(ctx context.Context, item domain.Item, query string, resolved actions.Resolved) (Document, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	current, err := s.search.Inspect(ctx, item.Target, item.RawPath())
	if err != nil {
		return Document{}, err
	}
	unchanged := sameMetadata(item, current)
	changed := (item.Size != nil && current.Size != nil && *item.Size != *current.Size) || (item.Modified != nil && current.Modified != nil && !item.Modified.Equal(*current.Modified)) || item.Kind != current.Kind
	original := item
	item.Size, item.Modified, item.Kind = current.Size, current.Modified, current.Kind
	s.mu.Lock()
	epoch := s.epoch[item.ID]
	s.mu.Unlock()
	keyData, _ := json.Marshal(struct {
		Version        int
		Item, Original domain.Item
		Query          string
		Resolved       actions.Resolved
		Epoch          uint64
		CustomPreview  bool
	}{2, item, original, query, resolved, epoch, s.custom[resolved.Preview]})
	key := string(keyData)
	cacheable := item.Kind != "symlink" && item.Kind != "other"
	if cacheable {
		if cached, ok := s.cache.Get(key); ok {
			var entry struct {
				Version  int
				Document Document
			}
			if json.Unmarshal([]byte(cached), &entry) == nil && entry.Version == 2 {
				return entry.Document, nil
			}
		}
	}
	doc := Document{Metadata: metadata(item, resolved), Snippets: boundedSnippets(item.Matches), Changed: changed}
	switch {
	case resolved.Preview != "":
		a, ok := resolved.Find(resolved.Preview)
		if !ok {
			return Document{}, fmt.Errorf("preview action %q is not configured", resolved.Preview)
		}
		if !a.Available {
			return Document{}, fmt.Errorf("preview: %s", a.Reason)
		}
		if a.Mode != "preview" {
			return Document{}, fmt.Errorf("action %q is not a preview action", a.ID)
		}
		argv, e := actions.Expand(a.Argv, item, query, resolved.GitRoot)
		if e != nil {
			return Document{}, e
		}
		cwd, e := actions.Expand([]string{a.Cwd}, item, query, resolved.GitRoot)
		if e != nil {
			return Document{}, e
		}
		start, end, builtin := builtinRange(a.Argv)
		builtin = builtin && a.ID == "bat" && !s.custom[a.ID] && !hasExtracted(item)
		if builtin {
			header, _, e := s.read(ctx, item, 4)
			if e != nil {
				return Document{}, e
			}
			if utf16BOM(header) || utf32BOM(header) {
				raw, truncated, e := s.read(ctx, item, MaxBytes)
				if e != nil {
					return Document{}, e
				}
				setContent(&doc, raw, 1, item.Matches, unchanged, truncated)
				// bat's plain-output ranges count raw byte newlines, which is unsafe for
				// UTF-16. Decode first and then apply the same source-line window.
				window := doc.Lines[:0]
				for _, line := range doc.Lines {
					if line.Number >= start && line.Number <= end {
						window = append(window, line)
					}
				}
				doc.Lines = window
			} else {
				raw, truncated, e := s.commandBytes(ctx, item.Target, argv, cwd[0], MaxBytes)
				if e != nil {
					return Document{}, e
				}
				setContent(&doc, raw, start, item.Matches, unchanged, truncated)
			}
		} else {
			raw, truncated, e := s.commandBytes(ctx, item.Target, argv, cwd[0], MaxBytes)
			if e != nil {
				return Document{}, e
			}
			doc.Opaque = clean(string(raw))
			doc.Truncated = truncated
		}
	case item.Kind == "directory" || item.Kind == "dir":
		doc.Opaque, err = directory(item)
	case hasExtracted(item):
		doc.Opaque = "Document matches use extracted text locations."
	case actions.IsTextMIME(resolved.MIME):
		var raw []byte
		var truncated bool
		raw, truncated, err = s.read(ctx, item, MaxBytes)
		if err == nil {
			setContent(&doc, raw, 1, item.Matches, unchanged, truncated)
		}
	default:
		doc.Opaque = "No text preview for this file type. Configure a preview rule to use another tool."
	}
	if err != nil {
		return Document{}, err
	}
	if err = ctx.Err(); err != nil {
		return Document{}, err
	}
	// A file may be edited while a command is producing its preview. Recheck
	// before attaching source locations and before caching the resulting body.
	if len(doc.Lines) > 0 {
		after, inspectErr := s.search.Inspect(ctx, item.Target, item.RawPath())
		if inspectErr != nil || !sameMetadata(item, after) {
			doc.Changed = true
			cacheable = false
			for i := range doc.Lines {
				doc.Lines[i].Spans = nil
			}
		}
	}
	if err = ctx.Err(); err != nil {
		return Document{}, err
	}
	boundDocument(&doc)
	// A failed disposable cache write must not discard a successfully read file.
	if cacheable {
		data, _ := json.Marshal(struct {
			Version  int
			Document Document
		}{2, doc})
		_ = s.cache.Put(key, string(data))
	}
	return doc, nil
}
func sameMetadata(a, b domain.Item) bool {
	return a.Size != nil && b.Size != nil && *a.Size == *b.Size && a.Modified != nil && b.Modified != nil && a.Modified.Equal(*b.Modified) && a.Kind == b.Kind
}

// A custom action named bat remains opaque. Only the built-in byte-preserving
// flags and simple source range qualify for native line mapping.
func builtinRange(argv []string) (start, end int, ok bool) {
	required := map[string]bool{"--no-config": false, "--color=never": false, "--style=plain": false, "--decorations=never": false, "--paging=never": false, "--wrap=never": false, "--tabs=0": false, "--strip-ansi=never": false}
	start, end = 1, 0
	for _, arg := range argv {
		if _, found := required[arg]; found {
			required[arg] = true
		}
		if value, found := strings.CutPrefix(arg, "--line-range="); found {
			first, last, found := strings.Cut(value, ":")
			if !found {
				return 0, 0, false
			}
			if first != "" {
				n, e := strconv.Atoi(first)
				if e != nil || n < 1 {
					return 0, 0, false
				}
				start = n
			}
			n, e := strconv.Atoi(last)
			if e != nil || n < start {
				return 0, 0, false
			}
			end = n
		}
	}
	for _, found := range required {
		if !found {
			return 0, 0, false
		}
	}
	return start, end, end >= start
}
func (s *Service) read(ctx context.Context, item domain.Item, limit int) ([]byte, bool, error) {
	if item.Kind != "file" && item.Kind != "symlink" {
		return nil, false, nil
	}
	if item.Target.Remote() {
		return s.commandBytes(ctx, item.Target, []string{"head", "-c", strconv.Itoa(limit + 1), item.RawPath()}, "", limit)
	}
	info, err := os.Stat(item.RawPath())
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, errors.New("no text preview for this special file")
	}
	f, err := os.Open(item.RawPath())
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(limit+1)))
	if len(data) > limit {
		return data[:limit], true, err
	}
	return data, false, err
}
func (s *Service) commandBytes(ctx context.Context, target domain.Target, argv []string, cwd string, limit int) ([]byte, bool, error) {
	var buf bytes.Buffer
	err := s.runner.Stream(ctx, target, argv, cwd, func(r io.Reader) error {
		_, e := io.Copy(&buf, io.LimitReader(r, int64(limit+1)))
		if e == nil && buf.Len() > limit {
			return errLimit
		}
		return e
	})
	if errors.Is(err, errLimit) {
		return buf.Bytes()[:limit], true, nil
	}
	return buf.Bytes(), false, err
}
func (s *Service) command(ctx context.Context, target domain.Target, argv []string, cwd string) (string, error) {
	raw, truncated, err := s.commandBytes(ctx, target, argv, cwd, MaxBytes)
	text := clean(string(raw))
	if truncated {
		text += "\n… preview truncated"
	}
	return text, err
}
func utf16BOM(data []byte) bool {
	return bytes.HasPrefix(data, []byte{0xff, 0xfe}) || bytes.HasPrefix(data, []byte{0xfe, 0xff})
}
func utf32BOM(data []byte) bool {
	return bytes.HasPrefix(data, []byte{0xff, 0xfe, 0, 0}) || bytes.HasPrefix(data, []byte{0, 0, 0xfe, 0xff})
}
func decode(data []byte, truncated, firstLine bool) (string, bool) {
	if firstLine && utf32BOM(data) {
		return "Unsupported UTF-32 encoding: native line preview unavailable.", false
	}
	if firstLine && utf16BOM(data) {
		var order binary.ByteOrder = binary.LittleEndian
		if data[0] == 0xfe {
			order = binary.BigEndian
		}
		data = data[2:]
		if len(data)%2 != 0 && !truncated {
			return "Invalid UTF-16 data: native line preview unavailable.", false
		}
		units := make([]uint16, len(data)/2)
		for i := range units {
			units[i] = order.Uint16(data[i*2:])
		}
		// The last code unit can be cut off by the byte cap, not malformed input.
		if truncated && len(units) > 0 && units[len(units)-1] >= 0xd800 && units[len(units)-1] <= 0xdbff {
			units = units[:len(units)-1]
		}
		for i := 0; i < len(units); i++ {
			u := units[i]
			if u >= 0xd800 && u <= 0xdbff {
				if i+1 >= len(units) || units[i+1] < 0xdc00 || units[i+1] > 0xdfff {
					return "Invalid UTF-16 data: native line preview unavailable.", false
				}
				i++
			} else if u >= 0xdc00 && u <= 0xdfff {
				return "Invalid UTF-16 data: native line preview unavailable.", false
			}
		}
		return string(utf16.Decode(units)), true
	}
	if firstLine {
		data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "Binary data: text preview suppressed.", false
	}
	if truncated && len(data) > 0 {
		start := len(data) - 1
		for start > 0 && !utf8.RuneStart(data[start]) {
			start--
		}
		if !utf8.FullRune(data[start:]) {
			data = data[:start]
		}
	}
	return string(data), true
}
func setContent(doc *Document, raw []byte, first int, matches []domain.Match, highlight, truncated bool) {
	content, native := decode(raw, truncated, first == 1)
	doc.Truncated = truncated
	if !native {
		doc.Opaque = content
		return
	}
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	budget := MaxBytes
	for i, rawLine := range lines {
		rawLine = strings.TrimSuffix(rawLine, "\r")
		number := first + i
		var spans []domain.Span
		if highlight {
			for _, m := range matches {
				if !m.Extracted && m.Line == number && m.RawSpan != nil && m.RawSpan.Start >= 0 && m.RawSpan.End <= len(rawLine) {
					spans = append(spans, *m.RawSpan)
				}
			}
		}
		text, mapped := domain.SanitizeSpans(rawLine, spans, budget)
		if text == "" && rawLine != "" {
			doc.Truncated = true
			break
		}
		doc.Lines = append(doc.Lines, Line{Number: number, Text: text, Spans: mapped})
		budget -= len(text) + 1
		if budget <= 0 {
			if i < len(lines)-1 || len(text) < len(rawLine) {
				doc.Truncated = true
			}
			break
		}
	}
}
func boundedSnippets(matches []domain.Match) []domain.Match {
	var result []domain.Match
	for _, m := range matches {
		if m.Text == "" {
			continue
		}
		m.Text, m.Spans = domain.ClipSpans(m.Text, m.Spans, 1024)
		result = append(result, m)
		if len(result) == 8 {
			break
		}
	}
	return result
}
func boundDocument(doc *Document) {
	budget := MaxBytes
	doc.Metadata, _ = domain.ClipSpans(doc.Metadata, nil, min(budget, 8192))
	budget -= len(doc.Metadata) + 1
	for i := range doc.Snippets {
		remaining := max(0, budget-64)
		if len(doc.Snippets[i].Text) > remaining {
			doc.Truncated = true
		}
		doc.Snippets[i].Text, doc.Snippets[i].Spans = domain.ClipSpans(doc.Snippets[i].Text, doc.Snippets[i].Spans, remaining)
		budget -= len(doc.Snippets[i].Text) + 64
	}
	if len(doc.Opaque) > max(0, budget) {
		doc.Truncated = true
	}
	doc.Opaque, _ = domain.ClipSpans(doc.Opaque, nil, max(0, budget))
	budget -= len(doc.Opaque)
	kept := doc.Lines[:0]
	for _, line := range doc.Lines {
		available := max(0, budget-16)
		if len(line.Text) > available {
			doc.Truncated = true
			line.Text, line.Spans = domain.ClipSpans(line.Text, line.Spans, available)
		}
		if available == 0 {
			doc.Truncated = true
			break
		}
		kept = append(kept, line)
		budget -= len(line.Text) + 16
	}
	doc.Lines = kept
}

// Text renders a plain compatibility view. The TUI colors the structured fields
// itself and can toggle line numbers without re-reading or executing anything.
func (doc Document) Text(lineNumbers bool) string {
	var b strings.Builder
	b.WriteString(doc.Metadata)
	b.WriteByte('\n')
	if doc.Changed {
		b.WriteString("File changed since search; original match highlights are unavailable.\n")
	}
	if len(doc.Snippets) > 0 {
		b.WriteString("Search matches:\n")
		for _, m := range doc.Snippets {
			label := m.Source + " line"
			if m.Extracted {
				label = m.Source + " extracted line"
			}
			fmt.Fprintf(&b, "%s %d: %s\n", label, m.Line, m.Text)
		}
	}
	b.WriteString(doc.Opaque)
	if doc.Opaque != "" {
		b.WriteByte('\n')
	}
	for _, line := range doc.Lines {
		if lineNumbers {
			fmt.Fprintf(&b, "%d │ ", line.Number)
		}
		b.WriteString(line.Text)
		b.WriteByte('\n')
	}
	if doc.Truncated {
		b.WriteString("… preview truncated\n")
	}
	return b.String()
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
	text := fmt.Sprintf("%s\n%s · %s · %s\nModified: %s\n", domain.Display(item.RawPath()), item.Kind, domain.HumanSize(item.Size), domain.Display(r.MIME), modified)
	if item.Target.Remote() {
		text += "Host: " + domain.Display(item.Target.Host) + "\n"
	}
	if r.GitRoot != "" {
		text += "Git: " + domain.Display(r.GitRoot) + "\n"
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
func clean(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i], _ = domain.SanitizeSpans(strings.TrimSuffix(line, "\r"), nil, MaxBytes)
	}
	return strings.Join(lines, "\n")
}
