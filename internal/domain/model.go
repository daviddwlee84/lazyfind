package domain

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

type Target struct {
	Host string `json:"host,omitempty" toml:"host"`
}

func (t Target) ID() string {
	if t.Host == "" {
		return "local"
	}
	return "ssh:" + t.Host
}
func (t Target) Remote() bool { return t.Host != "" }

type QuerySpec struct {
	Raw         string   `json:"raw"`
	Text        string   `json:"text"`
	Target      Target   `json:"target"`
	Roots       []string `json:"roots"`
	Sources     []string `json:"sources"`
	Regex       bool     `json:"regex"`
	FullPath    bool     `json:"full_path"`
	Filters     Filters  `json:"filters"`
	BaseFilters *Filters `json:"base_filters,omitempty"`
}
type Filters struct {
	Kinds          []string `json:"kinds,omitempty"`
	Extensions     []string `json:"extensions,omitempty"`
	Hidden         bool     `json:"hidden"`
	Ignored        bool     `json:"ignored"`
	Depth          int      `json:"depth,omitempty"`
	ModifiedWithin string   `json:"modified_within,omitempty"`
	After          string   `json:"after,omitempty"`
	Before         string   `json:"before,omitempty"`
	MinSize        *int64   `json:"min_size,omitempty"`
	MaxSize        *int64   `json:"max_size,omitempty"`
}
type Match struct {
	Source    string `json:"source"`
	Line      int    `json:"line,omitempty"`
	Column    int    `json:"column,omitempty"`
	Text      string `json:"text,omitempty"`
	Extracted bool   `json:"extracted,omitempty"`
	// RawSpan addresses the decoded search line before terminal sanitization.
	// Spans address the sanitized Text. Older snapshots omit both fields.
	RawSpan *Span  `json:"raw_span,omitempty"`
	Spans   []Span `json:"spans,omitempty"`
}
type Item struct {
	ID               string     `json:"id"`
	Path             string     `json:"path"`
	PathBytes        string     `json:"path_bytes,omitempty"`
	Name             string     `json:"name"`
	Kind             string     `json:"kind"`
	Target           Target     `json:"target"`
	Size             *int64     `json:"size,omitempty"`
	Modified         *time.Time `json:"modified,omitempty"`
	Sources          []string   `json:"sources"`
	Matches          []Match    `json:"matches,omitempty"`
	MatchCount       int        `json:"match_count"`
	MatchesTruncated bool       `json:"matches_truncated,omitempty"`
}

func (i Item) RawPath() string {
	if i.PathBytes != "" {
		if b, e := base64.StdEncoding.DecodeString(i.PathBytes); e == nil {
			return string(b)
		}
	}
	return i.Path
}
func ItemID(t Target, p string) string {
	return t.ID() + ":" + base64.RawURLEncoding.EncodeToString([]byte(p))
}

type Problem struct {
	Source  string `json:"source"`
	Message string `json:"message"`
}
type Run struct {
	SchemaVersion     int        `json:"schema_version"`
	ID                string     `json:"id"`
	ParentID          string     `json:"parent_id,omitempty"`
	Query             QuerySpec  `json:"query"`
	StartedAt         time.Time  `json:"started_at"`
	CapturedAt        time.Time  `json:"captured_at"`
	FinishedAt        *time.Time `json:"finished_at,omitempty"`
	ResolvedAfter     *time.Time `json:"resolved_after,omitempty"`
	ResolvedBefore    *time.Time `json:"resolved_before,omitempty"`
	Status            string     `json:"status"`
	Items             []Item     `json:"items"`
	Problems          []Problem  `json:"problems,omitempty"`
	Truncated         bool       `json:"truncated"`
	SnapshotTruncated bool       `json:"snapshot_truncated,omitempty"`
	Observed          int        `json:"observed"`
	Pinned            bool       `json:"pinned"`
}
type Event struct {
	Kind    string
	Item    *Item
	Problem *Problem
	Run     *Run
}

// Display escapes terminal controls without changing the path used for operations.
func Display(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '�'
		}
		return r
	}, strings.ToValidUTF8(s, "�"))
}
func SortItems(items []Item, field string, descending bool) {
	sort.SliceStable(items, func(a, b int) bool {
		x, y := items[a], items[b]
		c := 0
		switch field {
		case "size":
			if x.Size == nil || y.Size == nil {
				if x.Size == nil && y.Size != nil {
					return false
				}
				if x.Size != nil && y.Size == nil {
					return true
				}
			} else {
				if *x.Size < *y.Size {
					c = -1
				} else if *x.Size > *y.Size {
					c = 1
				}
			}
		case "modified":
			if x.Modified == nil || y.Modified == nil {
				if x.Modified == nil && y.Modified != nil {
					return false
				}
				if x.Modified != nil && y.Modified == nil {
					return true
				}
			} else {
				if x.Modified.Before(*y.Modified) {
					c = -1
				} else if x.Modified.After(*y.Modified) {
					c = 1
				}
			}
		case "path":
			c = strings.Compare(strings.ToLower(x.Path), strings.ToLower(y.Path))
		case "kind":
			c = strings.Compare(x.Kind, y.Kind)
		case "extension":
			c = strings.Compare(extension(x.Path), extension(y.Path))
		default:
			c = strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name))
		}
		if c == 0 {
			return x.ID < y.ID
		}
		if descending {
			return c > 0
		}
		return c < 0
	})
}
func extension(p string) string {
	return strings.ToLower(strings.TrimPrefix(filepath.Ext(p), "."))
}
func Fuzzy(s, q string) bool {
	s = strings.ToLower(s)
	q = strings.ToLower(q)
	for _, r := range q {
		j := strings.IndexRune(s, r)
		if j < 0 {
			return false
		}
		s = s[j+len(string(r)):]
	}
	return true
}
func HumanSize(n *int64) string {
	if n == nil {
		return "—"
	}
	v := float64(*n)
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", *n)
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}
