package search

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/daviddwlee84/lazyfind/internal/domain"
	"github.com/daviddwlee84/lazyfind/internal/transport"
)

func (s *Service) recent(ctx context.Context, q domain.QuerySpec, fd string, md metadata, c *collector, after, before *time.Time) error {
	tool := s.cfg.Tools.Zoxide
	if tool == "" {
		tool = "zoxide"
	}
	if !s.runner.Available(ctx, q.Target, tool) {
		return fmt.Errorf("%s is unavailable on %s", tool, q.Target.ID())
	}
	// zoxide's public output is newline-separated. Its database cannot faithfully
	// represent newline paths; fd-backed name/content search remains NUL-safe.
	var candidates []string
	totalBytes := 0
	err := s.runner.Stream(ctx, q.Target, []string{tool, "query", "--list"}, "", func(rd io.Reader) error {
		sc := bufio.NewScanner(rd)
		sc.Buffer(make([]byte, 4096), 1<<20)
		for sc.Scan() {
			totalBytes += len(sc.Text())
			if len(candidates) >= 10000 || totalBytes > 8<<20 {
				return fmt.Errorf("zoxide candidate limit (10000 entries / 8 MiB) exceeded")
			}
			if sc.Text() != "" {
				candidates = append(candidates, path.Clean(sc.Text()))
			}
		}
		return sc.Err()
	})
	if transport.ExitCode(err) == 1 {
		return nil
	}
	if err != nil {
		return err
	}
	// fd emits physical paths. Resolve zoxide's learned aliases on their own
	// host before checking scope, using bounded SSH argument batches.
	var physical []string
	if q.Target.Remote() {
		const script = `for p do (CDPATH= cd -P -- "$p" && printf '%s\000' "$PWD") 2>/dev/null || :; done`
		for start := 0; start < len(candidates); {
			end := start
			size := 0
			for end < len(candidates) && end-start < 64 && size < 24<<10 {
				size += len(candidates[end]) + 1
				end++
			}
			argv := append([]string{"sh", "-c", script, "lazyfind-recent"}, candidates[start:end]...)
			out, e := s.runner.Run(ctx, q.Target, argv, "")
			if e != nil {
				return e
			}
			for _, p := range bytes.Split(out, []byte{0}) {
				if len(p) > 0 {
					physical = append(physical, string(p))
				}
			}
			start = end
		}
	} else {
		for _, p := range candidates {
			resolved, e := filepath.EvalSymlinks(p)
			if e == nil {
				physical = append(physical, resolved)
			}
		}
	}
	var paths []string
	for _, p := range physical {
		if !insideRoots(p, q.Roots) {
			continue
		}
		if q.Filters.Depth > 0 && pathDepth(p, q.Roots) > q.Filters.Depth {
			continue
		}
		text := path.Base(p)
		if q.FullPath {
			text = p
		}
		if !smartMatch(text, q.Text, q.Regex) {
			continue
		}
		if !q.Filters.Hidden {
			visible := false
			// A nested root explicitly includes its own hidden ancestors.
			// Admit the directory when any selected root sees a visible path.
			for _, root := range q.Roots {
				if p == root {
					visible = true
					break
				}
				prefix := strings.TrimRight(root, "/") + "/"
				if !strings.HasPrefix(p, prefix) {
					continue
				}
				hidden := false
				for _, part := range strings.Split(strings.TrimPrefix(p, prefix), "/") {
					if strings.HasPrefix(part, ".") {
						hidden = true
						break
					}
				}
				if !hidden {
					visible = true
					break
				}
			}
			if !visible {
				continue
			}
		}
		paths = append(paths, p)
	}
	if len(paths) == 0 {
		return nil
	}
	// Use fd itself to apply ignore rules, exactly as for name and content
	// sources. Only candidate paths are retained, bounding memory by zoxide output.
	wanted := map[string]bool{}
	for _, p := range paths {
		wanted[p] = true
	}
	if !q.Filters.Ignored {
		allowed := map[string]bool{}
		filterQ := q
		filterQ.Filters.Kinds = []string{"directory"}
		filterQ.Regex = false
		filterQ.FullPath = false
		err = s.traverse(ctx, filterQ, fd, "", false, func(batch []string) error {
			for _, p := range batch {
				if wanted[p] {
					allowed[p] = true
				}
			}
			return ctx.Err()
		})
		if err != nil {
			return err
		}
		// A root itself is a valid recent directory but fd does not list root nodes.
		for _, r := range q.Roots {
			if wanted[r] {
				allowed[r] = true
			}
		}
		for p := range wanted {
			if !allowed[p] {
				delete(wanted, p)
			}
		}
	}
	paths = paths[:0]
	for p := range wanted {
		paths = append(paths, p)
	}
	for start := 0; start < len(paths); start += 64 {
		end := start + 64
		if end > len(paths) {
			end = len(paths)
		}
		items, err := md.read(ctx, paths[start:end])
		c.problem("recent metadata", err)
		for _, i := range items {
			if i.Kind == "directory" && accepts(i, q, after, before) {
				i.Sources = []string{"recent"}
				c.upsert(i)
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return nil
}
