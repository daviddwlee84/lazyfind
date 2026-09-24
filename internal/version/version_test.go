package version

import (
	"runtime/debug"
	"testing"
)

func TestVersionPrecedenceAndDevelopmentFallback(t *testing.T) {
	for _, tc := range []struct {
		name, injected, module, want string
		missing                      bool
	}{
		{name: "injected release wins", injected: "v0.1.1", module: "v0.1.0", want: "v0.1.1"},
		{name: "injected checkout identity", injected: "dev+bd4bb7c-dirty", module: "v0.1.0", want: "dev+bd4bb7c-dirty"},
		{name: "module install", module: "v0.1.1", want: "v0.1.1"},
		{name: "default dev allows module", injected: "dev", module: "v0.1.1", want: "v0.1.1"},
		{name: "devel placeholder allows module", injected: "(devel)", module: "v0.1.1", want: "v0.1.1"},
		{name: "pseudo module version", module: "v0.0.0-20260924000000-bd4bb7c00000", want: "v0.0.0-20260924000000-bd4bb7c00000"},
		{name: "trim whitespace", injected: "  v0.1.1\n", module: "v0.1.0", want: "v0.1.1"},
		{name: "unversioned checkout", injected: "dev", module: "(devel)", want: "dev"},
		{name: "unknown placeholders", injected: "unknown", module: "devel", want: "dev"},
		{name: "no build metadata", missing: true, want: "dev"},
		{name: "injected without metadata", injected: "v0.1.1", missing: true, want: "v0.1.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var info *debug.BuildInfo
			if !tc.missing {
				info = &debug.BuildInfo{Main: debug.Module{Version: tc.module}}
			}
			if got := resolve(tc.injected, info, !tc.missing); got != tc.want {
				t.Fatalf("version=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestCurrentHonorsInjectedVersion(t *testing.T) {
	if got := Current("v0.1.1-test"); got != "v0.1.1-test" {
		t.Fatalf("current version=%q", got)
	}
}
