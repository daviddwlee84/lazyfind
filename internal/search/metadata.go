package search

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/daviddwlee84/lazyfind/internal/domain"
	"github.com/daviddwlee84/lazyfind/internal/transport"
)

type metadata struct {
	runner *transport.Runner
	target domain.Target
}

func newItem(target domain.Target, p, kind string, size int64, modified time.Time) domain.Item {
	item := domain.Item{ID: domain.ItemID(target, p), Path: p, Name: path.Base(p), Kind: kind, Target: target, Modified: &modified}
	if !utf8.ValidString(p) {
		item.PathBytes = base64.StdEncoding.EncodeToString([]byte(p))
		item.Path = strings.ToValidUTF8(p, "�")
		item.Name = strings.ToValidUTF8(path.Base(p), "�")
	}
	if kind == "file" {
		item.Size = &size
	}
	return item
}
func (m metadata) read(ctx context.Context, paths []string) ([]domain.Item, error) {
	items := make([]domain.Item, 0, len(paths))
	if !m.target.Remote() {
		var first error
		for _, p := range paths {
			if ctx.Err() != nil {
				return items, ctx.Err()
			}
			fi, err := os.Lstat(p)
			if err != nil {
				if first == nil {
					first = err
				}
				continue
			}
			kind := "other"
			switch {
			case fi.Mode().IsRegular():
				kind = "file"
			case fi.IsDir():
				kind = "directory"
			case fi.Mode()&os.ModeSymlink != 0:
				kind = "symlink"
			}
			items = append(items, newItem(m.target, filepath.Clean(p), kind, fi.Size(), fi.ModTime()))
		}
		return items, first
	}
	// Metadata fields contain no filename; paths always use NUL framing.
	// Preserve nanoseconds so remote date filters match local os.Lstat exactly.
	script := `if LC_ALL=C stat -c '%f %s %y' / >/dev/null 2>&1; then flavor=g; else flavor=b; fi
 for p do
 printf '%s\000' "$p"
 if [ "$flavor" = g ]; then data=$(LC_ALL=C stat -c '%f %s %y' -- "$p" 2>/dev/null); else data=$(LC_ALL=C stat -f '%p %z %.9Fm' -- "$p" 2>/dev/null); fi
 if [ -n "$data" ]; then printf '%s %s\000' "$flavor" "$data"; else printf '\000'; fi
 done`
	argv := append([]string{"sh", "-c", script, "lazyfind-stat"}, paths...)
	out, err := m.runner.Run(ctx, m.target, argv, "")
	if err != nil {
		return nil, err
	}
	fields := bytes.Split(out, []byte{0})
	var first error
	for i := 0; i+1 < len(fields); i += 2 {
		p := string(fields[i])
		numeric := strings.Fields(string(fields[i+1]))
		if len(numeric) < 4 || (numeric[0] == "g" && len(numeric) != 6) || (numeric[0] == "b" && len(numeric) != 4) {
			if first == nil {
				first = fmt.Errorf("cannot stat %s", domain.Display(p))
			}
			continue
		}
		base := 16
		if numeric[0] == "b" {
			base = 8
		}
		mode, e1 := strconv.ParseUint(numeric[1], base, 32)
		size, e2 := strconv.ParseInt(numeric[2], 10, 64)
		modified, e3 := statModified(numeric[0], strings.Join(numeric[3:], " "))
		if e1 != nil || e2 != nil || e3 != nil {
			if first == nil {
				first = fmt.Errorf("invalid remote metadata for %s", domain.Display(p))
			}
			continue
		}
		kind := "other"
		switch mode & 0170000 {
		case 0100000:
			kind = "file"
		case 0040000:
			kind = "directory"
		case 0120000:
			kind = "symlink"
		}
		items = append(items, newItem(m.target, path.Clean(p), kind, size, modified))
	}
	return items, first
}

// statModified avoids floating point conversion, which rounds modern epoch
// values by hundreds of nanoseconds. BSD's F modifier prints tv_sec.tv_nsec;
// even for a negative tv_sec the fractional part is a positive nanosecond field
// (for example -1.500000000 denotes Unix(-1, 500000000), not -1.5 seconds).
func statModified(flavor, value string) (time.Time, error) {
	switch flavor {
	case "g":
		return time.Parse("2006-01-02 15:04:05.999999999 -0700", value)
	case "b":
		whole, fraction, hasFraction := strings.Cut(value, ".")
		seconds, err := strconv.ParseInt(whole, 10, 64)
		if err != nil {
			return time.Time{}, err
		}
		var nanoseconds int64
		if hasFraction {
			if fraction == "" || len(fraction) > 9 {
				return time.Time{}, fmt.Errorf("invalid fractional stat time %q", value)
			}
			for _, digit := range fraction {
				if digit < '0' || digit > '9' {
					return time.Time{}, fmt.Errorf("invalid fractional stat time %q", value)
				}
			}
			nanoseconds, err = strconv.ParseInt(fraction+strings.Repeat("0", 9-len(fraction)), 10, 64)
			if err != nil {
				return time.Time{}, err
			}
		}
		return time.Unix(seconds, nanoseconds), nil
	default:
		return time.Time{}, fmt.Errorf("unknown stat flavor %q", flavor)
	}
}

func accepts(item domain.Item, q domain.QuerySpec, after, before *time.Time) bool {
	f := q.Filters
	if len(f.Kinds) > 0 && !contains(f.Kinds, item.Kind) {
		return false
	}
	if len(f.Extensions) > 0 {
		ext := strings.TrimPrefix(strings.ToLower(path.Ext(item.RawPath())), ".")
		if !contains(f.Extensions, ext) {
			return false
		}
	}
	if after != nil && (item.Modified == nil || item.Modified.Before(*after)) {
		return false
	}
	if before != nil && (item.Modified == nil || !item.Modified.Before(*before)) {
		return false
	}
	if f.MinSize != nil && (item.Size == nil || *item.Size < *f.MinSize) {
		return false
	}
	if f.MaxSize != nil && (item.Size == nil || *item.Size > *f.MaxSize) {
		return false
	}
	return true
}
func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
func insideRoots(p string, roots []string) bool {
	for _, r := range roots {
		if p == r || strings.HasPrefix(p, strings.TrimRight(r, "/")+"/") {
			return true
		}
	}
	return false
}
func pathDepth(p string, roots []string) int {
	best := int(^uint(0) >> 1)
	for _, r := range roots {
		if p == r {
			return 0
		}
		if strings.HasPrefix(p, strings.TrimRight(r, "/")+"/") {
			n := strings.Count(strings.TrimPrefix(p, strings.TrimRight(r, "/")+"/"), "/") + 1
			if n < best {
				best = n
			}
		}
	}
	return best
}

// Inspect refreshes one item's metadata for explicit preview/action operations.
// History rendering must use its stored item instead of calling this method.
func (s *Service) Inspect(ctx context.Context, target domain.Target, p string) (domain.Item, error) {
	items, err := (metadata{runner: s.runner, target: target}).read(ctx, []string{p})
	if err != nil {
		return domain.Item{}, err
	}
	if len(items) == 0 {
		return domain.Item{}, fmt.Errorf("item is unavailable: %s", domain.Display(p))
	}
	return items[0], nil
}
