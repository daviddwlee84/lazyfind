package domain

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type queryToken struct {
	text    string
	literal bool
}

// queryTokens supports quoted phrases without interpreting shell expansion.
func queryTokens(raw string) ([]queryToken, error) {
	var tokens []queryToken
	var b strings.Builder
	var quote rune
	started, literal := false, false
	escaped := false
	flush := func() {
		if started {
			tokens = append(tokens, queryToken{b.String(), literal})
			b.Reset()
			started = false
			literal = false
		}
	}
	runes := []rune(raw)
	for index, r := range runes {
		if escaped {
			b.WriteRune(r)
			escaped = false
			started = true
			continue
		}
		if r == '\\' && quote != '\'' && index+1 < len(runes) && (unicode.IsSpace(runes[index+1]) || runes[index+1] == '\\' || runes[index+1] == '"' || runes[index+1] == '\'') {
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
			continue
		}
		if r == '\'' || r == '"' {
			if !started {
				literal = true
			}
			quote = r
			started = true
			continue
		}
		if unicode.IsSpace(r) {
			flush()
			continue
		}
		b.WriteRune(r)
		started = true
	}
	if escaped {
		b.WriteRune('\\')
	}
	if quote != 0 {
		return nil, fmt.Errorf("unclosed quote in query")
	}
	flush()
	return tokens, nil
}

var qualifiers = map[string]bool{"type": true, "ext": true, "mtime": true, "after": true, "before": true, "size": true, "hidden": true, "ignored": true, "depth": true}

func qualifier(t queryToken) (string, string, bool) {
	if t.literal {
		return "", "", false
	}
	k, v, ok := strings.Cut(t.text, ":")
	return k, v, ok && qualifiers[k]
}

// ParseQuery overlays inline qualifiers on a preserved copy of the non-query
// settings. Removing a qualifier restores the configured/form/CLI baseline.
func ParseQuery(raw string, base QuerySpec) (QuerySpec, error) {
	q := base
	q.Raw = raw
	baseline := base.Filters
	if base.BaseFilters != nil {
		baseline = *base.BaseFilters
	}
	baseline.Kinds = append([]string(nil), baseline.Kinds...)
	baseline.Extensions = append([]string(nil), baseline.Extensions...)
	q.BaseFilters = &baseline
	q.Filters = baseline
	q.Filters.Kinds = append([]string(nil), baseline.Kinds...)
	q.Filters.Extensions = append([]string(nil), baseline.Extensions...)
	tokens, err := queryTokens(raw)
	if err != nil {
		return q, err
	}
	var words []string
	for _, t := range tokens {
		key, value, ok := qualifier(t)
		if !ok {
			words = append(words, t.text)
			continue
		}
		if value == "" {
			return q, fmt.Errorf("%s requires a value", key)
		}
		switch key {
		case "type":
			q.Filters.Kinds = nil
			for _, kind := range strings.Split(value, ",") {
				switch strings.ToLower(kind) {
				case "f", "file":
					kind = "file"
				case "d", "dir", "directory":
					kind = "directory"
				case "l", "link", "symlink":
					kind = "symlink"
				default:
					return q, fmt.Errorf("unknown type %q; use file, dir, or symlink", kind)
				}
				q.Filters.Kinds = appendUnique(q.Filters.Kinds, kind)
			}
		case "ext":
			q.Filters.Extensions = nil
			for _, ext := range strings.Split(value, ",") {
				ext = strings.TrimPrefix(strings.ToLower(ext), ".")
				if ext == "" || strings.ContainsAny(ext, "/\\") {
					return q, fmt.Errorf("invalid extension %q", ext)
				}
				q.Filters.Extensions = appendUnique(q.Filters.Extensions, ext)
			}
		case "mtime":
			value = strings.TrimPrefix(value, "<")
			if _, err := ParseDuration(value); err != nil {
				return q, fmt.Errorf("mtime: %w", err)
			}
			q.Filters.ModifiedWithin = value
		case "after", "before":
			if _, err := ParseDate(value); err != nil {
				return q, fmt.Errorf("%s: use YYYY-MM-DD or an RFC3339 timestamp", key)
			}
			if key == "after" {
				q.Filters.After = value
			} else {
				q.Filters.Before = value
			}
		case "size":
			op := byte('=')
			if strings.ContainsAny(value[:1], "<>=") {
				op = value[0]
				value = value[1:]
			}
			inclusive := false
			if strings.HasPrefix(value, "=") {
				inclusive = true
				value = value[1:]
			}
			n, err := ParseSize(value)
			if err != nil {
				return q, err
			}
			switch op {
			case '>':
				if !inclusive {
					if n == math.MaxInt64 {
						return q, fmt.Errorf("size exceeds supported range")
					}
					n++
				}
				q.Filters.MinSize = &n
			case '<':
				if !inclusive {
					n--
				}
				if n < 0 {
					return q, fmt.Errorf("size cannot be negative")
				}
				q.Filters.MaxSize = &n
			default:
				q.Filters.MinSize = &n
				m := n
				q.Filters.MaxSize = &m
			}
		case "hidden", "ignored":
			v, err := strconv.ParseBool(value)
			if err != nil {
				return q, fmt.Errorf("%s requires true or false", key)
			}
			if key == "hidden" {
				q.Filters.Hidden = v
			} else {
				q.Filters.Ignored = v
			}
		case "depth":
			v, err := strconv.Atoi(value)
			if err != nil || v < 1 {
				return q, fmt.Errorf("depth requires a positive integer")
			}
			q.Filters.Depth = v
		}
	}
	q.Text = strings.Join(words, " ")
	if err := ValidateQuery(q); err != nil {
		return q, err
	}
	return q, nil
}
func appendUnique(xs []string, x string) []string {
	for _, s := range xs {
		if s == x {
			return xs
		}
	}
	return append(xs, x)
}
func ValidateQuery(q QuerySpec) error {
	if q.Regex {
		if _, err := regexp.Compile(q.Text); err != nil {
			return fmt.Errorf("invalid regex: %w", err)
		}
	}
	if q.Filters.Depth < 0 {
		return fmt.Errorf("depth cannot be negative")
	}
	if q.Filters.ModifiedWithin != "" {
		if _, err := ParseDuration(q.Filters.ModifiedWithin); err != nil {
			return err
		}
	}
	var a, b time.Time
	var err error
	if q.Filters.After != "" {
		a, err = ParseDate(q.Filters.After)
		if err != nil {
			return err
		}
	}
	if q.Filters.Before != "" {
		b, err = ParseDate(q.Filters.Before)
		if err != nil {
			return err
		}
	}
	if !a.IsZero() && !b.IsZero() && !a.Before(b) {
		return fmt.Errorf("after must be earlier than before")
	}
	if q.Filters.MinSize != nil && *q.Filters.MinSize < 0 || q.Filters.MaxSize != nil && *q.Filters.MaxSize < 0 {
		return fmt.Errorf("size cannot be negative")
	}
	if q.Filters.MinSize != nil && q.Filters.MaxSize != nil && *q.Filters.MinSize > *q.Filters.MaxSize {
		return fmt.Errorf("minimum size exceeds maximum size")
	}
	for _, k := range q.Filters.Kinds {
		if k != "file" && k != "directory" && k != "symlink" {
			return fmt.Errorf("unknown file type %q", k)
		}
	}
	for _, s := range q.Sources {
		switch s {
		case "names", "text", "documents", "recent":
		default:
			return fmt.Errorf("unknown source %q", s)
		}
	}
	return nil
}
func ParseDate(s string) (time.Time, error) {
	if len(s) == 10 {
		return time.ParseInLocation("2006-01-02", s, time.Local)
	}
	return time.Parse(time.RFC3339, s)
}

// ParseDuration adds day/week units to Go's duration syntax.
func ParseDuration(s string) (time.Duration, error) {
	if len(s) > 1 {
		unit := s[len(s)-1]
		if unit == 'd' || unit == 'w' {
			n, err := strconv.ParseFloat(s[:len(s)-1], 64)
			factor := float64(24 * time.Hour)
			if unit == 'w' {
				factor *= 7
			}
			if err != nil || n <= 0 || math.IsNaN(n) || math.IsInf(n, 0) || n*factor >= float64(math.MaxInt64) {
				return 0, fmt.Errorf("invalid duration %q", s)
			}
			return time.Duration(n * factor), nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid duration %q (example: 7d, 2w, 12h)", s)
	}
	return d, nil
}

var sizeRE = regexp.MustCompile(`(?i)^([0-9]+(?:\.[0-9]+)?)(B|KB|MB|GB|TB|KIB|MIB|GIB|TIB)?$`)

func ParseSize(s string) (int64, error) {
	m := sizeRE.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("invalid size %q (example: 10MiB)", s)
	}
	n, _ := strconv.ParseFloat(m[1], 64)
	unit := strings.ToUpper(m[2])
	power := 0
	switch unit {
	case "KB", "KIB":
		power = 1
	case "MB", "MIB":
		power = 2
	case "GB", "GIB":
		power = 3
	case "TB", "TIB":
		power = 4
	}
	base := 1000.0
	if strings.Contains(unit, "I") {
		base = 1024
	}
	n *= math.Pow(base, float64(power))
	if n >= float64(math.MaxInt64) {
		return 0, fmt.Errorf("size exceeds supported range")
	}
	return int64(n), nil
}
