# Configuration

`lazyfind config init` writes a complete default TOML without overwriting an existing
file. `config path` works with malformed config; `config edit` validates after the
editor exits. `config show` prints effective settings as JSON.

File selection: `--config` → `LAZYFIND_CONFIG` → XDG default. Explicit flags override
file settings, including `--hidden=false`. Relative XDG values are ignored. Reads do
not create directories. Unknown fields and key conflicts are rejected; no project-local
config is loaded implicitly.

```toml
[search]
sources = ["names", "text"]
hidden = false
ignored = false
max_results = 5000
max_matches = 50
timeout_seconds = 60
debounce_ms = 200

[ui]
mouse = true
preview = true
color = "auto" # honors NO_COLOR; always or never overrides

[history]
enabled = true
max_days = 90 # 0 = unlimited age
max_runs = 1000 # 0 = unlimited count
max_results = 5000
snippets_per_item = 2
snippet_bytes = 512
snippet_total_bytes = 262144

[cache]
max_bytes = 268435456 # 0 disables disk preview caching

[inventory]
dev = false
fleet = false

[[roots]]
name = "Projects"
paths = ["~/Projects", "~/Documents"]

[[roots]]
name = "Remote data"
host = "workstation"
paths = ["/srv/data", "~/experiments"]

[[hosts]]
name = "Workstation"
alias = "workstation"

[keymap]
search = "/"
actions = ":"
history = "H"
```

`[tools]` can override the executable names/paths `fd`, `rg`, `rga`, `zoxide`, `ssh`.
Remote executable paths refer to the remote machine. fd falls back to fdfind.

## Actions

Built-ins: `editor`, `yazi`, `lazygit`, `glow`, `bat`, `open`, `copy`, `shell`.
Custom definitions override the same ID. Rules contribute an ordered union of
actions; the first matching explicit default and preview win. Rules can match
`kinds`, `extensions`, MIME glob, `git = true`, and `location = "local"` or
`"remote"`. Unavailable actions show a reason and cannot run.

```toml
[[actions]]
id = "markdown"
label = "Read Markdown with glow"
argv = ["glow", "{path}"]
mode = "suspend"

[[actions]]
id = "parquet-preview"
label = "Inspect Parquet schema"
argv = ["parquet-tools", "inspect", "{path}"]
mode = "preview"

[[rules]]
extensions = ["md", "markdown"]
actions = ["markdown", "editor", "bat"]
default = "editor"

[[rules]]
extensions = ["parquet"]
actions = ["parquet-preview"]
preview = "parquet-preview"

[[rules]]
kinds = ["directory"]
git = true
actions = ["lazygit", "yazi", "editor"]
default = "lazygit"
```

Arguments retain boundaries. Placeholders: `{path}`, `{dir}`, `{name}`, `{ext}`,
`{git_root}`, `{query}`, `{line}`. `cwd` supports the same expansion. `$VISUAL` and
`$EDITOR` are parsed into executable and quoted arguments without shell evaluation.
Shell evaluation is never implicit; explicitly configured shell commands are trusted
user configuration, so avoid interpolating paths/query into shell source.

`suspend` hands the terminal to a child and returns; `replace` exits after successful
child completion; `detach` launches a GUI/background child; `preview` captures bounded
output. Location defaults to the item's target; a local or remote location restricts
availability. Local-only openers are unavailable for remote paths.

History can contain snippets and cache can contain preview text. State files and
directories are private. Use `--no-history` or disable `[history].enabled` when
snapshots are unwanted.
