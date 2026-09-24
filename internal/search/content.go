package search

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/daviddwlee84/lazyfind/internal/domain"
	"github.com/daviddwlee84/lazyfind/internal/transport"
)

type rgText struct {
	Text  *string `json:"text"`
	Bytes string  `json:"bytes"`
}

func (t rgText) decode() (string, error) {
	if t.Text != nil {
		return *t.Text, nil
	}
	if t.Bytes != "" {
		b, err := base64.StdEncoding.DecodeString(t.Bytes)
		return string(b), err
	}
	return "", nil
}

type rgEvent struct {
	Type string `json:"type"`
	Data struct {
		Path         rgText `json:"path"`
		Lines        rgText `json:"lines"`
		LineNumber   int    `json:"line_number"`
		BinaryOffset *int64 `json:"binary_offset"`
		Submatches   []struct {
			Start int `json:"start"`
			End   int `json:"end"`
		} `json:"submatches"`
	} `json:"data"`
}

var errLargeRecord = errors.New("rg JSON record exceeds 8 MiB; that record was skipped")

// readRecord is bounded even for arbitrarily long input lines. It consumes an
// oversized record completely so subsequent small records are still usable.
func readRecord(r *bufio.Reader) ([]byte, error) {
	const limit = 8 << 20
	var out []byte
	large := false
	for {
		piece, err := r.ReadSlice('\n')
		if len(out)+len(piece) > limit {
			large = true
			out = nil
		}
		if !large {
			out = append(out, piece...)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if large {
			return nil, errLargeRecord
		}
		if err == io.EOF && len(out) > 0 {
			return out, nil
		}
		return out, err
	}
}
func snippet(s string) string {
	s = strings.TrimRight(s, "\r\n")
	if len(s) > 2048 {
		s = s[:2048]
		for !utf8.ValidString(s) && len(s) > 0 {
			s = s[:len(s)-1]
		}
		s += "…"
	}
	return domain.Display(s)
}
func (s *Service) content(ctx context.Context, q domain.QuerySpec, tool, source string, items []domain.Item, maxMatches int, c *collector) error {
	argv := []string{tool, "--json", "--no-config", "--color=never", "--smart-case", "--line-number", "--with-filename", "--max-count", strconv.Itoa(maxMatches + 1)}
	if !q.Regex {
		argv = append(argv, "--fixed-strings")
	}
	argv = append(argv, "--", q.Text)
	byPath := make(map[string]domain.Item, len(items))
	for _, item := range items {
		argv = append(argv, item.RawPath())
		byPath[item.RawPath()] = item
	}
	pending := map[string]domain.Item{}
	err := s.runner.Stream(ctx, q.Target, argv, "", func(rd io.Reader) error {
		br := bufio.NewReaderSize(rd, 64<<10)
		for {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			line, err := readRecord(br)
			if errors.Is(err, errLargeRecord) {
				c.problem(source, err)
				continue
			}
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			if len(strings.TrimSpace(string(line))) == 0 {
				continue
			}
			var event rgEvent
			if err = json.Unmarshal(line, &event); err != nil {
				return fmt.Errorf("decode %s JSON: %w", source, err)
			}
			p, err := event.Data.Path.decode()
			if err != nil {
				return fmt.Errorf("decode %s path: %w", source, err)
			}
			p = path.Clean(p)
			switch event.Type {
			case "match":
				item, ok := pending[p]
				if !ok {
					item, ok = byPath[p]
					if !ok {
						c.problem(source, fmt.Errorf("backend returned a path outside its candidate batch: %s", domain.Display(p)))
						continue
					}
					item.Sources = []string{source}
				}
				text, err := event.Data.Lines.decode()
				if err != nil {
					return fmt.Errorf("decode %s text: %w", source, err)
				}
				text = snippet(text)
				n := len(event.Data.Submatches)
				if n == 0 {
					n = 1
				}
				item.MatchCount += n
				for i := 0; i < n; i++ {
					if len(item.Matches) >= maxMatches {
						item.MatchesTruncated = true
						break
					}
					col := 1
					if i < len(event.Data.Submatches) {
						col = event.Data.Submatches[i].Start + 1
					}
					item.Matches = append(item.Matches, domain.Match{Source: source, Line: event.Data.LineNumber, Column: col, Text: text, Extracted: source == "documents"})
				}
				pending[p] = item
			case "end":
				item, ok := pending[p]
				delete(pending, p)
				// rg can emit a match before discovering NUL later in the file. Commit
				// only after its end record confirms this was not a binary match.
				if ok && event.Data.BinaryOffset == nil {
					c.upsert(item)
				}
			}
		}
	})
	if transport.ExitCode(err) == 1 {
		return nil
	}
	return err
}
