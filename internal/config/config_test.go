package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func isolated(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("LAZYFIND_CONFIG", "")
	return dir
}
func writeConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMissingDefaultHasNoSideEffects(t *testing.T) {
	dir := isolated(t)
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Search.MaxResults != 5000 || cfg.History.SnippetTotalBytes != 256*1024 || !cfg.UI.Mouse {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("read created directories: %v", entries)
	}
	if !strings.HasSuffix(cfg.Paths.Config, "config/lazyfind/config.toml") {
		t.Fatalf("wrong XDG config: %s", cfg.Paths.Config)
	}
}
func TestPatchDefaultsAndExplicitOptOut(t *testing.T) {
	isolated(t)
	cfg := Defaults()
	if !cfg.Search.AutoSearchEmpty || cfg.UI.InitialFocus != "search" || !cfg.UI.HighlightMatches || !cfg.UI.PreviewLineNumbers || cfg.DirectoryUsage.Concurrency != 2 || cfg.DirectoryUsage.TimeoutSeconds != 60 {
		t.Fatal("patch defaults differ from agreed behavior")
	}
	p := filepath.Join(t.TempDir(), "config.toml")
	writeConfig(t, p, "[search]\nauto_search_empty=false\nmax_results=12\n[ui]\ninitial_focus='results'\nhighlight_matches=false\npreview_line_numbers=false\n[directory_usage]\nconcurrency=1\ntimeout_seconds=2\n")
	got, e := Load(p)
	if e != nil {
		t.Fatal(e)
	}
	if got.Search.AutoSearchEmpty || got.UI.InitialFocus != "results" || got.UI.HighlightMatches || got.UI.PreviewLineNumbers || got.Search.MaxResults != 12 || got.DirectoryUsage.Concurrency != 1 {
		t.Fatal("explicit preferences were lost")
	}
	cfg.Keymap["quit"] = "ctrl+@"
	if Validate(cfg) == nil {
		t.Fatal("legacy CtrlSpace alias escaped conflict checks")
	}
}

func TestCustomActionKeysCannotShadowNavigationOrCommands(t *testing.T) {
	isolated(t)
	for _, key := range []string{"h", "l", "pgdown", "ctrl+i", "n", "q"} {
		cfg := Defaults()
		cfg.Actions = []Action{{ID: "test", Argv: []string{"true"}, Key: key}}
		if Validate(cfg) == nil {
			t.Fatalf("accepted conflicting key %s", key)
		}
	}
	cfg := Defaults()
	cfg.Actions = []Action{{ID: "test", Argv: []string{"true"}, Key: "e"}}
	if err := Validate(cfg); err != nil {
		t.Fatal(err)
	}
}
func TestPathAndFlagPrecedence(t *testing.T) {
	dir := isolated(t)
	envPath := filepath.Join(dir, "env.toml")
	explicitPath := filepath.Join(dir, "explicit.toml")
	writeConfig(t, envPath, "[search]\nmax_results = 17\n")
	writeConfig(t, explicitPath, "[search]\nmax_results = 29\n[ui]\nmouse = false\n[keymap]\nquit = 'x'\n")
	t.Setenv("LAZYFIND_CONFIG", envPath)
	env, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if env.Search.MaxResults != 17 {
		t.Fatalf("environment file not loaded: %+v", env.Search)
	}
	explicit, err := Load(explicitPath)
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Search.MaxResults != 29 || explicit.UI.Mouse {
		t.Fatalf("explicit config not respected: %+v", explicit)
	}
	if explicit.Keymap["quit"] != "x" || explicit.Keymap["help"] != "?" {
		t.Fatalf("partial keymap should overlay defaults: %v", explicit.Keymap)
	}
}
func TestRelativeXDGIgnoredAndMissingHomeRejected(t *testing.T) {
	dir := isolated(t)
	t.Setenv("XDG_STATE_HOME", "relative")
	paths, err := ResolvePaths("")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "home/.local/state/lazyfind")
	if paths.State != want {
		t.Fatalf("state=%q, want %q", paths.State, want)
	}
	t.Setenv("HOME", "")
	if _, err = ResolvePaths(""); err == nil {
		t.Fatal("missing HOME should fail")
	}
}
func TestInvalidConfigurationIsActionable(t *testing.T) {
	dir := isolated(t)
	for _, tc := range []struct{ name, body, want string }{
		{"unknown", "[search]\nmax_result = 1\n", "unknown"},
		{"limit", "[search]\nmax_results = 0\n", "must be positive"},
		{"source", "[search]\nsources = ['unknown']\n", "unknown search source"},
		{"reserved", "[keymap]\nquit = 'enter'\n", "reserved"},
		{"conflict", "[keymap]\nquit = '?'\n", "both use"},
		{"missing-action", "[[rules]]\nactions = ['missing']\n", "unknown action"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name+".toml")
			writeConfig(t, path, tc.body)
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
		})
	}
	if _, err := Load(filepath.Join(dir, "absent.toml")); err == nil {
		t.Fatal("explicit missing config should fail")
	}
	t.Setenv("LAZYFIND_CONFIG", filepath.Join(dir, "absent.toml"))
	if _, err := Load(""); err == nil {
		t.Fatal("environment missing config should fail")
	}
}
func TestInitRoundTripAndNeverOverwrite(t *testing.T) {
	isolated(t)
	cfg := Defaults()
	if err := Init(cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Search.MaxResults != cfg.Search.MaxResults {
		t.Fatal("default did not round trip")
	}
	info, err := os.Stat(cfg.Paths.Config)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("config mode is %o", info.Mode().Perm())
	}
	before, err := os.ReadFile(cfg.Paths.Config)
	if err != nil {
		t.Fatal(err)
	}
	if err = Init(cfg); err == nil {
		t.Fatal("existing config should not be overwritten")
	}
	after, err := os.ReadFile(cfg.Paths.Config)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("existing config was changed")
	}
}
