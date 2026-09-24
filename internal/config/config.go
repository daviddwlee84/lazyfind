package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
)

// DefaultKeymap binds semantic actions. Text input consumes printable keys before
// this map is consulted; the remaining navigation keys are always available.
func DefaultKeymap() map[string]string {
	return map[string]string{
		"quit": "q", "search": "/", "actions": ":", "help": "?",
		"roots": "r", "history": "H", "filters": "f", "preview": "p",
		"refresh": "ctrl+r", "result_filter": "ctrl+f", "sort": "s",
		"sources": "S", "mouse": "f2", "next_match": "n", "previous_match": "N",
		"insert_search": "i", "complete_query": "ctrl+space", "preview_lines": "L",
		"copy_menu": "y", "directory_usage": "u",
	}
}

// Defaults returns independent defaults, resolving paths without creating them.
// Load should be used when path errors must be reported to the caller.
func Defaults() Config {
	paths, _ := ResolvePaths("")
	return Config{
		Search:         Search{Sources: []string{"names", "text"}, MaxResults: 5000, MaxMatches: 50, TimeoutSeconds: 60, DebounceMS: 200, AutoSearchEmpty: true},
		UI:             UI{Mouse: true, Preview: true, Color: "auto", InitialFocus: "search", HighlightMatches: true, PreviewLineNumbers: true},
		DirectoryUsage: DirectoryUsage{Concurrency: 2, TimeoutSeconds: 60},
		History:        History{Enabled: true, MaxDays: 90, MaxRuns: 1000, MaxResults: 5000, SnippetsPerItem: 2, SnippetBytes: 512, SnippetTotalBytes: 256 * 1024},
		Cache:          Cache{MaxBytes: 256 * 1024 * 1024},
		Tools:          Tools{FD: "fd", RG: "rg", RGA: "rga", Zoxide: "zoxide", SSH: "ssh"},
		Keymap:         DefaultKeymap(), Paths: paths,
	}
}

// ResolvePaths deliberately uses XDG on macOS as well as Linux. Relative XDG
// variables are ignored according to the XDG base-directory specification.
func ResolvePaths(explicit string) (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return Paths{}, errors.New("resolve lazyfind paths: a valid absolute HOME is required")
	}
	xdg := func(name, fallback string) string {
		if value := os.Getenv(name); filepath.IsAbs(value) {
			return value
		}
		return filepath.Join(home, fallback)
	}
	selected := explicit
	if selected == "" {
		selected = os.Getenv("LAZYFIND_CONFIG")
	}
	if selected == "" {
		selected = filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), "lazyfind", "config.toml")
	}
	if selected == "~" {
		selected = home
	} else if strings.HasPrefix(selected, "~/") {
		selected = filepath.Join(home, selected[2:])
	}
	selected, err = filepath.Abs(selected)
	if err != nil {
		return Paths{}, fmt.Errorf("resolve config path: %w", err)
	}
	return Paths{
		Config: selected,
		State:  filepath.Join(xdg("XDG_STATE_HOME", ".local/state"), "lazyfind"),
		Cache:  filepath.Join(xdg("XDG_CACHE_HOME", ".cache"), "lazyfind"),
		Data:   filepath.Join(xdg("XDG_DATA_HOME", ".local/share"), "lazyfind"),
	}, nil
}

// Load reads defaults and the selected TOML file without making directories.
func Load(explicitPath string) (Config, error) {
	cfg := Defaults()
	paths, err := ResolvePaths(explicitPath)
	if err != nil {
		return Config{}, err
	}
	cfg.Paths = paths
	b, err := os.ReadFile(paths.Config)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && explicitPath == "" && os.Getenv("LAZYFIND_CONFIG") == "" {
			return cfg, Validate(cfg)
		}
		return Config{}, fmt.Errorf("read config %s: %w", paths.Config, err)
	}
	if err := toml.NewDecoder(bytes.NewReader(b)).DisallowUnknownFields().Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", paths.Config, err)
	}
	if err := Validate(cfg); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", paths.Config, err)
	}
	return cfg, nil
}

func Validate(cfg Config) error {
	if !oneOf(cfg.UI.InitialFocus, "search", "results") {
		return errors.New("ui.initial_focus must be search or results")
	}
	if cfg.DirectoryUsage.Concurrency < 1 || cfg.DirectoryUsage.Concurrency > 16 || cfg.DirectoryUsage.TimeoutSeconds < 1 {
		return errors.New("directory_usage.concurrency must be 1–16 and timeout_seconds must be positive")
	}
	if cfg.Search.MaxResults < 1 || cfg.Search.MaxMatches < 1 || cfg.Search.TimeoutSeconds < 1 || cfg.Search.DebounceMS < 0 {
		return errors.New("search.max_results, max_matches and timeout_seconds must be positive; debounce_ms must be nonnegative")
	}
	if len(cfg.Search.Sources) == 0 {
		return errors.New("search.sources must enable at least one source")
	}
	seenSources := map[string]bool{}
	for _, source := range cfg.Search.Sources {
		if !oneOf(source, "names", "text", "documents", "recent") {
			return fmt.Errorf("unknown search source %q (use names, text, documents or recent)", source)
		}
		if seenSources[source] {
			return fmt.Errorf("duplicate search source %q", source)
		}
		seenSources[source] = true
	}
	if !oneOf(cfg.UI.Color, "auto", "always", "never") {
		return errors.New("ui.color must be auto, always or never")
	}
	if cfg.History.MaxDays < 0 || cfg.History.MaxRuns < 0 || cfg.History.MaxResults < 1 || cfg.History.SnippetsPerItem < 0 || cfg.History.SnippetBytes < 0 || cfg.History.SnippetTotalBytes < 0 {
		return errors.New("history limits must be nonnegative and history.max_results must be positive")
	}
	if cfg.Cache.MaxBytes < 0 {
		return errors.New("cache.max_bytes must be nonnegative")
	}
	for _, tool := range []struct{ name, path string }{{"fd", cfg.Tools.FD}, {"rg", cfg.Tools.RG}, {"rga", cfg.Tools.RGA}, {"zoxide", cfg.Tools.Zoxide}, {"ssh", cfg.Tools.SSH}} {
		if strings.TrimSpace(tool.path) == "" || strings.ContainsRune(tool.path, 0) {
			return fmt.Errorf("tools.%s must be a nonempty executable name or path", tool.name)
		}
	}
	rootNames := map[string]bool{}
	for _, root := range cfg.Roots {
		if strings.TrimSpace(root.Name) == "" || rootNames[root.Name] {
			return fmt.Errorf("root names must be nonempty and unique: %q", root.Name)
		}
		rootNames[root.Name] = true
		if len(root.Paths) == 0 {
			return fmt.Errorf("root %q needs at least one path", root.Name)
		}
		for _, path := range root.Paths {
			if strings.TrimSpace(path) == "" || strings.ContainsRune(path, 0) {
				return fmt.Errorf("root %q has an invalid path", root.Name)
			}
		}
	}
	hostNames := map[string]bool{}
	for _, host := range cfg.Hosts {
		if strings.TrimSpace(host.Name) == "" || hostNames[host.Name] {
			return fmt.Errorf("host names must be nonempty and unique: %q", host.Name)
		}
		hostNames[host.Name] = true
		if strings.TrimSpace(host.Alias) == "" || strings.HasPrefix(host.Alias, "-") || strings.IndexFunc(host.Alias, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
			return fmt.Errorf("host %q has an invalid SSH alias", host.Name)
		}
	}
	actions := map[string]bool{"editor": true, "yazi": true, "lazygit": true, "glow": true, "bat": true, "open": true, "copy": true, "shell": true}
	customIDs := map[string]bool{}
	actionKeys := map[string]string{}
	for _, action := range cfg.Actions {
		if action.Key != "" {
			if !validKey(action.Key) || oneOf(action.Key, "tab", "shift+tab", "enter", "esc", "up", "down", "left", "right", "ctrl+c", "ctrl+i", "ctrl+m", "j", "k", "h", "l", "g", "G", "home", "end", "pgup", "pgdown") {
				return fmt.Errorf("action %q uses invalid or reserved key %q", action.ID, action.Key)
			}
			for name, key := range cfg.Keymap {
				if CanonicalKey(action.Key) == CanonicalKey(key) {
					return fmt.Errorf("action %q key conflicts with keymap.%s", action.ID, name)
				}
			}
			if previous, ok := actionKeys[CanonicalKey(action.Key)]; ok {
				return fmt.Errorf("actions %q and %q share key %q", previous, action.ID, action.Key)
			}
			actionKeys[CanonicalKey(action.Key)] = action.ID
		}
		if strings.TrimSpace(action.ID) == "" || customIDs[action.ID] {
			return fmt.Errorf("custom action IDs must be nonempty and unique: %q", action.ID)
		}
		customIDs[action.ID] = true
		actions[action.ID] = true
		if len(action.Argv) == 0 || strings.TrimSpace(action.Argv[0]) == "" {
			return fmt.Errorf("action %q needs argv with an executable", action.ID)
		}
		for _, arg := range action.Argv {
			if strings.ContainsRune(arg, 0) {
				return fmt.Errorf("action %q has a NUL in argv", action.ID)
			}
		}
		if !oneOf(action.Mode, "", "preview", "suspend", "replace", "detach") {
			return fmt.Errorf("action %q has invalid mode %q", action.ID, action.Mode)
		}
		if !oneOf(action.Location, "", "local", "remote") {
			return fmt.Errorf("action %q has invalid location %q", action.ID, action.Location)
		}
	}
	for index, rule := range cfg.Rules {
		if !oneOf(rule.Location, "", "local", "remote") {
			return fmt.Errorf("rule %d has invalid location %q", index+1, rule.Location)
		}
		for _, kind := range rule.Kinds {
			if !oneOf(kind, "file", "dir", "directory", "symlink") {
				return fmt.Errorf("rule %d has invalid kind %q", index+1, kind)
			}
		}
		refs := append(append([]string{}, rule.Actions...), rule.Default, rule.Preview)
		for _, id := range refs {
			if id != "" && !actions[id] {
				return fmt.Errorf("rule %d refers to unknown action %q", index+1, id)
			}
		}
	}
	return validateKeymap(cfg.Keymap)
}

func validateKeymap(keymap map[string]string) error {
	defaults := DefaultKeymap()
	reserved := []string{"tab", "shift+tab", "enter", "esc", "up", "down", "left", "right", "ctrl+c", "ctrl+i", "ctrl+m", "j", "k", "h", "l", "g", "G", "home", "end", "pgup", "pgdown"}
	seen := map[string]string{}
	names := make([]string, 0, len(keymap))
	for name := range keymap {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		key := keymap[name]
		if _, ok := defaults[name]; !ok {
			return fmt.Errorf("unknown keymap action %q", name)
		}
		if key == "" {
			return fmt.Errorf("keymap.%s must not be empty", name)
		}
		if oneOf(key, reserved...) {
			return fmt.Errorf("keymap.%s uses reserved navigation key %q", name, key)
		}
		if !validKey(key) {
			return fmt.Errorf("keymap.%s has unsupported key %q", name, key)
		}
		if previous, ok := seen[CanonicalKey(key)]; ok {
			return fmt.Errorf("keymap.%s and keymap.%s both use %q", previous, name, key)
		}
		seen[CanonicalKey(key)] = name
	}
	for name := range defaults {
		if keymap[name] == "" {
			return fmt.Errorf("missing keymap action %q", name)
		}
	}
	return nil
}

func validKey(key string) bool {
	if key == "ctrl+space" {
		return true
	}
	if utf8.RuneCountInString(key) == 1 {
		return true
	}
	if oneOf(key, "space", "backspace", "delete", "insert", "pgup", "pgdown") {
		return true
	}
	for _, prefix := range []string{"ctrl+", "alt+"} {
		if strings.HasPrefix(key, prefix) && utf8.RuneCountInString(strings.TrimPrefix(key, prefix)) == 1 {
			return true
		}
	}
	for n := 1; n <= 12; n++ {
		if key == fmt.Sprintf("f%d", n) {
			return true
		}
	}
	return false
}

// Ctrl+Space and NUL/Ctrl+@ are the same physical key in legacy terminals.
func CanonicalKey(key string) string {
	if key == "ctrl+@" || key == "ctrl+ " {
		return "ctrl+space"
	}
	return key
}
func oneOf(value string, options ...string) bool {
	for _, option := range options {
		if value == option {
			return true
		}
	}
	return false
}

// Init only creates a missing file. Existing config is never rewritten, so its
// comments and unrelated user edits survive subsequent invocations.
func Init(cfg Config) error {
	if cfg.Paths.Config == "" {
		return errors.New("config path is empty")
	}
	if err := Validate(cfg); err != nil {
		return err
	}
	b, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	const examples = `
# Named scopes can include several roots on one host:
# [[roots]]
# name = "projects"
# paths = ["~/Projects"]
# host = "" # empty means local; otherwise an OpenSSH alias
#
# [[hosts]]
# name = "Build server"
# alias = "build"
#
# Custom commands are argv arrays; placeholders never invoke an implicit shell.
# [[actions]]
# id = "parquet"
# label = "Inspect Parquet"
# argv = ["pqsum", "{path}"]
# mode = "suspend"
#
# [[rules]]
# extensions = ["parquet"]
# actions = ["parquet"]
# default = "parquet"
`
	if err = os.MkdirAll(filepath.Dir(cfg.Paths.Config), 0700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	f, err := os.OpenFile(cfg.Paths.Config, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("config already exists: %s", cfg.Paths.Config)
		}
		return fmt.Errorf("create config: %w", err)
	}
	_, writeErr := f.Write(append(append([]byte("# lazyfind configuration; see `lazyfind config show` for effective values.\n"), b...), []byte(examples)...))
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return fmt.Errorf("write config: %w", writeErr)
	}
	return closeErr
}
