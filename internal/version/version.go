// Package version resolves human-readable build identity without invoking Git at
// runtime. Release builds can inject a version; module installs carry their own.
package version

import (
	"runtime/debug"
	"strings"
)

func Current(injected string) string {
	info, ok := debug.ReadBuildInfo()
	return resolve(injected, info, ok)
}

func resolve(injected string, info *debug.BuildInfo, ok bool) string {
	if value := meaningful(injected); value != "" {
		return value
	}
	if ok && info != nil {
		if value := meaningful(info.Main.Version); value != "" {
			return value
		}
	}
	return "dev"
}

func meaningful(value string) string {
	value = strings.TrimSpace(value)
	switch value {
	case "", "dev", "devel", "(devel)", "unknown":
		return ""
	}
	return value
}
