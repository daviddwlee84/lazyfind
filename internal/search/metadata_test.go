package search

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/daviddwlee84/lazyfind/internal/config"
	"github.com/daviddwlee84/lazyfind/internal/domain"
)

func TestStatModifiedPreservesNanoseconds(t *testing.T) {
	for _, test := range []struct {
		flavor, value  string
		seconds, nanos int64
	}{
		{"b", "1790247094.613177513", 1790247094, 613177513},
		{"b", "1700000000.000000001", 1700000000, 1},
		{"b", "1700000000.999999999", 1700000000, 999999999},
		{"b", "1700000000.12", 1700000000, 120000000},
		{"b", "1700000000", 1700000000, 0},
		{"b", "-1.500000000", -1, 500000000},
		{"g", "2023-11-15 06:13:20.123456789 +0800", 1700000000, 123456789},
		{"g", "2023-11-14 22:13:20.000000001 +0000", 1700000000, 1},
		{"g", "2023-11-14 14:13:20.999999999 -0800", 1700000000, 999999999},
	} {
		t.Run(test.flavor+test.value, func(t *testing.T) {
			got, err := statModified(test.flavor, test.value)
			want := time.Unix(test.seconds, test.nanos)
			if err != nil || !got.Equal(want) {
				t.Fatalf("got %s (%d,%d), want %s: %v", got.Format(time.RFC3339Nano), got.Unix(), got.Nanosecond(), want.Format(time.RFC3339Nano), err)
			}
		})
	}
	for _, value := range []string{"", ".5", "1.", "1.1234567890", "1.-1", "1.NaN"} {
		if _, err := statModified("b", value); err == nil {
			t.Fatalf("accepted invalid fractional timestamp %q", value)
		}
	}
}

func TestRemoteStatMatchesLocalFractionalTimeBoundaries(t *testing.T) {
	// Fake SSH provides the remote shell boundary while the real target-side stat
	// binary reads the same fixture. Test both platform BSD and installed GNU stat.
	native, err := exec.LookPath("stat")
	if err != nil {
		t.Skip("stat is unavailable")
	}
	tools := map[string]string{"native": native}
	if gnu, err := exec.LookPath("gstat"); err == nil {
		tools["gnu"] = gnu
	}
	for name, stat := range tools {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			p := filepath.Join(root, "quote'\nprecision.txt")
			if err := os.WriteFile(p, []byte("needle"), 0600); err != nil {
				t.Fatal(err)
			}
			desired := time.Unix(1700000000, 613177513)
			if err := os.Chtimes(p, desired, desired); err != nil {
				t.Fatal(err)
			}
			bin := t.TempDir()
			if err := os.Symlink(stat, filepath.Join(bin, "stat")); err != nil {
				t.Fatal(err)
			}
			ssh := filepath.Join(bin, "ssh")
			script := `#!/bin/sh
while [ "$#" -gt 0 ]; do if [ "$1" = -- ]; then shift; shift; break; fi; shift; done
exec sh -c "$1"
`
			if err := os.WriteFile(ssh, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			service := New(config.Config{Tools: config.Tools{SSH: ssh}})
			local, err := service.Inspect(context.Background(), domain.Target{}, p)
			if err != nil {
				t.Fatal(err)
			}
			remote, err := service.Inspect(context.Background(), domain.Target{Host: "fixture"}, p)
			if err != nil {
				t.Fatal(err)
			}
			if local.Modified == nil || remote.Modified == nil || !remote.Modified.Equal(*local.Modified) || remote.RawPath() != p {
				t.Fatalf("mtime/path lost: local=%+v remote=%+v", local, remote)
			}
			if local.Modified.Nanosecond() == 0 {
				t.Skip("filesystem does not retain fractional mtimes")
			}
			equal := *local.Modified
			before := equal.Add(-time.Nanosecond)
			after := equal.Add(time.Nanosecond)
			for _, bounds := range []struct {
				name          string
				after, before *time.Time
				want          bool
			}{
				{"inclusive-after", &equal, nil, true}, {"one-nanosecond-after", &after, nil, false},
				{"exclusive-before", nil, &equal, false}, {"one-nanosecond-before", nil, &after, true},
				{"relative-cutoff-inside-second", &before, nil, true},
			} {
				t.Run(bounds.name, func(t *testing.T) {
					a := accepts(local, domain.QuerySpec{}, bounds.after, bounds.before)
					b := accepts(remote, domain.QuerySpec{}, bounds.after, bounds.before)
					if a != bounds.want || b != a {
						t.Fatalf("local=%v remote=%v want=%v", a, b, bounds.want)
					}
				})
			}
		})
	}
}
