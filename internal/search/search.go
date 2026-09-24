// Package search coordinates existing finding tools. It owns execution and
// merging; the TUI and CLI use the same service and query model.
package search

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/daviddwlee84/lazyfind/internal/config"
	"github.com/daviddwlee84/lazyfind/internal/domain"
	"github.com/daviddwlee84/lazyfind/internal/transport"
)

type Service struct {
	cfg    config.Config
	runner *transport.Runner
}

func New(cfg config.Config) *Service { return &Service{cfg: cfg, runner: transport.New(cfg)} }
func runID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

var errLimit = errors.New("result limit reached")

type collector struct {
	mu                     sync.Mutex
	run                    *domain.Run
	items                  map[string]domain.Item
	emit                   func(domain.Event)
	cancel                 context.CancelFunc
	maxResults, maxMatches int
	problemKeys            map[string]bool
}

func (c *collector) problem(source string, err error) {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, errLimit) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := source + ":" + err.Error()
	if len(c.run.Problems) >= 100 {
		return
	}
	if len(c.run.Problems) == 99 {
		source = "search"
		err = errors.New("additional errors omitted after 99 problems")
		key = "problem-limit"
	}
	if c.problemKeys[key] {
		return
	}
	c.problemKeys[key] = true
	p := domain.Problem{Source: source, Message: domain.Display(err.Error())}
	c.run.Problems = append(c.run.Problems, p)
	if c.emit != nil {
		c.emit(domain.Event{Kind: "problem", Problem: &p})
	}
}
func (c *collector) upsert(item domain.Item) {
	c.mu.Lock()
	defer c.mu.Unlock()
	existing, found := c.items[item.ID]
	if !found {
		if len(c.items) >= c.maxResults {
			c.run.Truncated = true
			c.run.Observed = len(c.items) + 1
			c.cancel()
			return
		}
		existing = item
		existing.Sources = append([]string(nil), item.Sources...)
		existing.Matches = append([]domain.Match(nil), item.Matches...)
	} else {
		newSource := false
		for _, source := range item.Sources {
			if !contains(existing.Sources, source) {
				existing.Sources = append(existing.Sources, source)
				newSource = true
			}
		}
		// Each source commits a complete per-file result. Repeated paths from
		// overlapping roots must not inflate counts or publish duplicate rows.
		if !newSource {
			return
		}
		existing.MatchCount += item.MatchCount
		for _, m := range item.Matches {
			duplicate := false
			for _, old := range existing.Matches {
				if old.Source == m.Source && old.Line == m.Line && old.Column == m.Column && old.Text == m.Text {
					duplicate = true
					break
				}
			}
			if !duplicate {
				if len(existing.Matches) < c.maxMatches {
					existing.Matches = append(existing.Matches, m)
				} else {
					existing.MatchesTruncated = true
				}
			}
		}
		existing.MatchesTruncated = existing.MatchesTruncated || item.MatchesTruncated
	}
	if len(existing.Matches) > c.maxMatches {
		existing.Matches = existing.Matches[:c.maxMatches]
		existing.MatchesTruncated = true
	}
	c.items[item.ID] = existing
	if !c.run.Truncated {
		c.run.Observed = len(c.items)
	}
	if c.emit != nil {
		eventItem := existing
		eventItem.Sources = append([]string(nil), existing.Sources...)
		eventItem.Matches = append([]domain.Match(nil), existing.Matches...)
		c.emit(domain.Event{Kind: "item", Item: &eventItem})
	}
}
func (s *Service) Execute(parent context.Context, q domain.QuerySpec, emit func(domain.Event)) domain.Run {
	started := time.Now()
	run := domain.Run{SchemaVersion: 1, ID: runID(), Query: q, StartedAt: started, CapturedAt: started, Status: "running", Items: []domain.Item{}}
	if len(q.Sources) == 0 {
		q.Sources = append([]string(nil), s.cfg.Search.Sources...)
		if len(q.Sources) == 0 {
			q.Sources = []string{"names", "text"}
		}
	}
	run.Query = q
	if emit != nil {
		header := run
		emit(domain.Event{Kind: "start", Run: &header})
	}
	timeout := s.cfg.Search.TimeoutSeconds
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	if timeout > 0 {
		var stop context.CancelFunc
		ctx, stop = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		defer stop()
	}
	maxResults, maxMatches := s.cfg.Search.MaxResults, s.cfg.Search.MaxMatches
	if maxResults <= 0 {
		maxResults = 5000
	}
	if maxMatches <= 0 {
		maxMatches = 50
	}
	c := &collector{run: &run, items: map[string]domain.Item{}, emit: emit, cancel: cancel, maxResults: maxResults, maxMatches: maxMatches, problemKeys: map[string]bool{}}
	finish := func() domain.Run {
		now := time.Now()
		run.CapturedAt = now
		run.FinishedAt = &now
		run.Items = make([]domain.Item, 0, len(c.items))
		for _, i := range c.items {
			run.Items = append(run.Items, i)
		}
		domain.SortItems(run.Items, "name", false)
		switch {
		case run.Truncated:
			run.Status = "truncated"
		case parent.Err() != nil:
			run.Status = "cancelled"
		case ctx.Err() != nil:
			run.Status = "cancelled"
		case len(run.Problems) > 0 && len(run.Items) > 0:
			run.Status = "partial"
		case len(run.Problems) > 0:
			run.Status = "failed"
		case len(run.Items) == 0:
			run.Status = "empty"
		default:
			run.Status = "complete"
		}
		return run
	}
	if err := domain.ValidateQuery(q); err != nil {
		c.problem("query", err)
		return finish()
	}
	if q.Filters.ModifiedWithin != "" {
		d, _ := domain.ParseDuration(q.Filters.ModifiedWithin)
		t := started.Add(-d)
		run.ResolvedAfter = &t
	}
	if q.Filters.After != "" {
		t, _ := domain.ParseDate(q.Filters.After)
		if run.ResolvedAfter == nil || t.After(*run.ResolvedAfter) {
			run.ResolvedAfter = &t
		}
	}
	if q.Filters.Before != "" {
		t, _ := domain.ParseDate(q.Filters.Before)
		run.ResolvedBefore = &t
	}
	if run.ResolvedAfter != nil && run.ResolvedBefore != nil && !run.ResolvedAfter.Before(*run.ResolvedBefore) {
		c.problem("query", errors.New("resolved time range is empty"))
		return finish()
	}
	roots, err := s.runner.ResolveRoots(ctx, q.Target, q.Roots)
	if err != nil {
		c.problem("scope", err)
		return finish()
	}
	q.Roots = roots
	run.Query = q
	// A resolved header lets consumers keep the exact remote/local scope and time
	// bounds even when they persist a snapshot before the first result arrives.
	if emit != nil {
		header := run
		emit(domain.Event{Kind: "scope", Run: &header})
	}
	fd := s.cfg.Tools.FD
	if fd == "" {
		fd = "fd"
	}
	hasFD := s.runner.Available(ctx, q.Target, fd)
	if !hasFD && fd == "fd" && s.runner.Available(ctx, q.Target, "fdfind") {
		fd = "fdfind"
		hasFD = true
	}
	needsFD := contains(q.Sources, "names") || contains(q.Sources, "text") || contains(q.Sources, "documents")
	if needsFD && !hasFD {
		c.problem("fd", fmt.Errorf("%s is unavailable on %s; install fd (or fdfind)", fd, q.Target.ID()))
		needsFD = false
	}
	md := metadata{runner: s.runner, target: q.Target}
	var wg sync.WaitGroup
	if needsFD && contains(q.Sources, "names") {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.traverse(ctx, q, fd, q.Text, false, func(paths []string) error {
				items, err := md.read(ctx, paths)
				c.problem("metadata", err)
				for _, item := range items {
					if accepts(item, q, run.ResolvedAfter, run.ResolvedBefore) {
						item.Sources = []string{"names"}
						c.upsert(item)
					}
				}
				return ctx.Err()
			})
			c.problem("names", err)
		}()
	}
	maySearchFiles := len(q.Filters.Kinds) == 0 || contains(q.Filters.Kinds, "file")
	if needsFD && maySearchFiles && q.Text != "" && (contains(q.Sources, "text") || contains(q.Sources, "documents")) {
		textTool, docsTool := s.cfg.Tools.RG, s.cfg.Tools.RGA
		if textTool == "" {
			textTool = "rg"
		}
		if docsTool == "" {
			docsTool = "rga"
		}
		doText, doDocs := contains(q.Sources, "text"), contains(q.Sources, "documents")
		if doText && !s.runner.Available(ctx, q.Target, textTool) {
			c.problem("text", fmt.Errorf("%s is unavailable on %s", textTool, q.Target.ID()))
			doText = false
		}
		if doDocs && !s.runner.Available(ctx, q.Target, docsTool) {
			c.problem("documents", fmt.Errorf("%s is unavailable on %s", docsTool, q.Target.ID()))
			doDocs = false
		}
		if doText || doDocs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				err := s.traverse(ctx, q, fd, "", true, func(paths []string) error {
					items, err := md.read(ctx, paths)
					c.problem("metadata", err)
					var texts, docs []domain.Item
					for _, item := range items {
						if item.Kind != "file" || !accepts(item, q, run.ResolvedAfter, run.ResolvedBefore) {
							continue
						}
						if isDocument(item.RawPath()) {
							if doDocs {
								docs = append(docs, item)
							}
						} else if doText {
							texts = append(texts, item)
						}
					}
					if len(texts) > 0 {
						c.problem("text", s.content(ctx, q, textTool, "text", texts, maxMatches, c))
					}
					if len(docs) > 0 && ctx.Err() == nil {
						c.problem("documents", s.content(ctx, q, docsTool, "documents", docs, maxMatches, c))
					}
					return ctx.Err()
				})
				c.problem("content candidates", err)
			}()
		}
	}
	// An empty keyword lists paths, even when the user last selected content only.
	if needsFD && q.Text == "" && !contains(q.Sources, "names") && (contains(q.Sources, "text") || contains(q.Sources, "documents")) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.traverse(ctx, q, fd, "", false, func(paths []string) error {
				items, err := md.read(ctx, paths)
				c.problem("metadata", err)
				for _, i := range items {
					if accepts(i, q, run.ResolvedAfter, run.ResolvedBefore) {
						i.Sources = []string{"names"}
						c.upsert(i)
					}
				}
				return ctx.Err()
			})
			c.problem("names", err)
		}()
	}
	if contains(q.Sources, "recent") {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.problem("recent", s.recent(ctx, q, fd, md, c, run.ResolvedAfter, run.ResolvedBefore))
		}()
	}
	wg.Wait()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		c.problem("search", fmt.Errorf("search timed out after %d seconds", timeout))
	}
	return finish()
}
func (s *Service) traverse(ctx context.Context, q domain.QuerySpec, fd, keyword string, filesOnly bool, consume func([]string) error) error {
	argv := []string{fd, "--color=never", "--absolute-path", "--print0"}
	if q.Filters.Hidden {
		argv = append(argv, "--hidden")
	}
	if q.Filters.Ignored {
		argv = append(argv, "--no-ignore")
	}
	if q.Filters.Depth > 0 {
		argv = append(argv, "--max-depth", strconv.Itoa(q.Filters.Depth))
	}
	if filesOnly {
		if len(q.Filters.Kinds) > 0 && !contains(q.Filters.Kinds, "file") {
			return nil
		}
		argv = append(argv, "--type", "f")
	} else {
		for _, k := range q.Filters.Kinds {
			switch k {
			case "file":
				argv = append(argv, "--type", "f")
			case "directory":
				argv = append(argv, "--type", "d")
			case "symlink":
				argv = append(argv, "--type", "l")
			}
		}
	}
	if !q.Regex {
		argv = append(argv, "--fixed-strings")
	}
	if q.FullPath {
		argv = append(argv, "--full-path")
	}
	argv = append(argv, "--", keyword)
	argv = append(argv, q.Roots...)
	return s.runner.Stream(ctx, q.Target, argv, "", func(rd io.Reader) error {
		br := bufio.NewReaderSize(rd, 64<<10)
		batch := make([]string, 0, 64)
		bytesInBatch := 0
		flush := func() error {
			if len(batch) == 0 {
				return nil
			}
			err := consume(batch)
			batch = make([]string, 0, 64)
			bytesInBatch = 0
			return err
		}
		for {
			raw, err := readPath(br)
			if len(raw) > 0 {
				if raw[len(raw)-1] != 0 {
					return errors.New("fd returned an unterminated path")
				}
				p := path.Clean(raw[:len(raw)-1])
				batch = append(batch, p)
				bytesInBatch += len(p) + 1
				if len(batch) >= 64 || bytesInBatch >= 24<<10 {
					if e := flush(); e != nil {
						return e
					}
				}
			}
			if err == io.EOF {
				return flush()
			}
			if err != nil {
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
	})
}

var documentExtensions = map[string]bool{"pdf": true, "epub": true, "docx": true, "odt": true, "pptx": true, "xlsx": true, "rtf": true, "mobi": true, "fb2": true, "ipynb": true, "zip": true, "tar": true, "gz": true, "bz2": true, "xz": true, "7z": true, "sqlite": true, "sqlite3": true, "db": true}

func isDocument(p string) bool {
	return documentExtensions[strings.TrimPrefix(strings.ToLower(path.Ext(p)), ".")]
}
func smartMatch(text, query string, regex bool) bool {
	if !regex {
		if strings.ToLower(query) == query {
			return strings.Contains(strings.ToLower(text), query)
		}
		return strings.Contains(text, query)
	}
	if strings.ToLower(query) == query {
		query = "(?i)" + query
	}
	re, err := regexp.Compile(query)
	return err == nil && re.MatchString(text)
}

// readPath rejects malformed backend output without unbounded allocation.
func readPath(r *bufio.Reader) (string, error) {
	var out []byte
	for {
		chunk, err := r.ReadSlice(0)
		if len(out)+len(chunk) > 1<<20 {
			return "", errors.New("fd path record exceeds 1 MiB")
		}
		out = append(out, chunk...)
		if err == bufio.ErrBufferFull {
			continue
		}
		return string(out), err
	}
}
