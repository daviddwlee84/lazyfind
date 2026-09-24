package domain

import (
	"reflect"
	"testing"
	"time"
)

func TestParseQueryQualifiersAndLiteralText(t *testing.T) {
	q, err := ParseQuery(`orderbook type:file,dir ext:.md,PDF mtime:<7d after:2026-09-01 before:2026-10-01 size:>10MiB size:<=20MiB hidden:true ignored:1 depth:3 owner:me`, QuerySpec{})
	if err != nil {
		t.Fatal(err)
	}
	if q.Text != "orderbook owner:me" {
		t.Fatalf("text %q", q.Text)
	}
	if !reflect.DeepEqual(q.Filters.Kinds, []string{"file", "directory"}) || !reflect.DeepEqual(q.Filters.Extensions, []string{"md", "pdf"}) {
		t.Fatalf("filters %+v", q.Filters)
	}
	if q.Filters.ModifiedWithin != "7d" || q.Filters.MinSize == nil || *q.Filters.MinSize != 10*1024*1024+1 || q.Filters.MaxSize == nil || *q.Filters.MaxSize != 20*1024*1024 || !q.Filters.Hidden || !q.Filters.Ignored || q.Filters.Depth != 3 {
		t.Fatalf("filters %+v", q.Filters)
	}
}
func TestParseQueryPreservesBaselineAndRemovesInlineConditions(t *testing.T) {
	base := QuerySpec{Filters: Filters{Hidden: true, Extensions: []string{"go"}}}
	first, err := ParseQuery(`needle type:dir hidden:false ext:md`, base)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ParseQuery(`needle`, first)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Filters.Kinds) != 0 || !second.Filters.Hidden || !reflect.DeepEqual(second.Filters.Extensions, []string{"go"}) {
		t.Fatalf("stale inline filters: %+v", second.Filters)
	}
	if !base.Filters.Hidden || base.Filters.Extensions[0] != "go" {
		t.Fatal("base mutated")
	}
}
func TestParseQueryQuotesAndRegexEscapes(t *testing.T) {
	q, err := ParseQuery(`"type:dir" "two words" foo\ bar 'ext:md'`, QuerySpec{})
	if err != nil {
		t.Fatal(err)
	}
	if q.Text != "type:dir two words foo bar ext:md" || len(q.Filters.Kinds) > 0 {
		t.Fatalf("%+v", q)
	}
	q, err = ParseQuery(`\d+\.txt`, QuerySpec{Regex: true})
	if err != nil {
		t.Fatal(err)
	}
	if q.Text != `\d+\.txt` {
		t.Fatalf("regex changed %q", q.Text)
	}
}
func TestInvalidQueries(t *testing.T) {
	for _, raw := range []string{`type:`, `type:potato`, `ext:a,`, `mtime:<bad`, `mtime:0d`, `mtime:NaNd`, `after:2026-02-30`, `after:2026-10-01 before:2026-09-01`, `size:>7MiB size:<1MiB`, `size:<0`, `size:1XB`, `size:>`, `hidden:maybe`, `depth:-1`, `"unclosed`} {
		t.Run(raw, func(t *testing.T) {
			if _, err := ParseQuery(raw, QuerySpec{}); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	if _, err := ParseQuery(`[`, QuerySpec{Regex: true}); err == nil {
		t.Fatal("invalid regex accepted")
	}
}
func TestDurationDateAndSize(t *testing.T) {
	d, err := ParseDuration("1.5d")
	if err != nil || d != 36*time.Hour {
		t.Fatalf("%s %v", d, err)
	}
	n, err := ParseSize("1.5MiB")
	if err != nil || n != 1572864 {
		t.Fatalf("%d %v", n, err)
	}
	n, err = ParseSize("10MB")
	if err != nil || n != 10000000 {
		t.Fatalf("%d %v", n, err)
	}
	if _, err = ParseDate("2026-09-24T18:00:00+08:00"); err != nil {
		t.Fatal(err)
	}
}
