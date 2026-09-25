# lazyfind

A search-first TUI over **fd, ripgrep, ripgrep-all and zoxide**. Start with a
keyword, refine the scope and filters, inspect one row per file, then choose
the right downstream tool. Search locally or on one SSH host at a time.

## Install / 安裝

```sh
brew install daviddwlee84/tap/lazyfind
lazyfind --version
lazyfind upgrade --check
```

**v0.1.2** adds macOS/Linux amd64/arm64 binary releases and the personal Homebrew
formula. Go is optional for binary installs; runtime backends remain separate.
See [installation, completion and owner-aware upgrades](docs/distribution.md).
[MIT license](LICENSE).

## Run

Build with Go 1.26+. Search requires `fd` (or `fdfind`) and `rg` on the selected
machine. macOS and Linux are supported. `rga`, `zoxide`, `bat`, `glow`, `yazi`
and `lazygit` are optional.

Install the tagged source version:

```sh
go install github.com/daviddwlee84/lazyfind@v0.1.2
lazyfind
```

Or build a checkout of [the public repository](https://github.com/daviddwlee84/lazyfind):

```sh
git clone https://github.com/daviddwlee84/lazyfind.git
cd lazyfind
make build
./bin/lazyfind
./bin/lazyfind ~/Projects ~/Documents --query 'orderbook'
./bin/lazyfind --host workstation --root /srv/projects
```

To install this checkout, run `go install .`. For checkout builds,
update the checkout and run `go install .` again to upgrade.
Prebuilt binaries and verified Homebrew upgrades are also available; see
[distribution](docs/distribution.md).
`make build` embeds the exact local tag, or `dev+COMMIT` with `-dirty` for tracked
changes. Use `make build VERSION=v0.1.1` for an explicit build identity. Tagged Go
module installs use embedded module metadata; plain unversioned builds show `dev`.

## Search and refine

Names and text content are enabled by default. Documents and frequent directories
are explicit extra sources. Zoxide also supplies root suggestions; its directory
history is separate from lazyfind's search snapshots.
Local startup searches the current roots even with an empty query and focuses the
search field. Both behaviors are configurable; remote searches still require Enter.

```text
orderbook
TODO ext:go,rs mtime:<7d
type:dir
report after:2026-09-01 before:2026-10-01
size:>10MiB type:file
"literal type:dir"
```

Keywords use literal substring matching and smart case. Regex and full-path
matching are explicit settings. Conditions are ANDed; comma-separated kinds or
extensions are ORed. Dates use local midnight or RFC3339; after is inclusive and
before exclusive. Relative dates are resolved once per run. Hidden and ignored
entries are excluded by default; symlinks are not followed. fd determines content
candidates too: `.fdignore` and `.gitignore` apply; `.rgignore` does not independently
change those explicit candidates.

One row represents one path on one target, combining all sources and content hits.
Sort by name, path, kind, extension, size or modification time without searching
again. The separate fuzzy result filter narrows the current list. Matching text is
highlighted and previews show line numbers by default.

Directory metadata size remains unknown. Press `u` to measure disk usage for the
selected directory or visible directories, then sort by the separate usage column.
This explicitly runs recursive `du` locally or remotely: hidden/ignored children
are included and symlinks are not followed. Complete measurements are cached for
the TUI session; use Recalculate to refresh them. They are not saved in history.

| Key | Action |
| --- | --- |
| `/` / `i` | Focus search; printable keys type normally |
| Ctrl+Space | Complete query conditions; Enter/Tab inserts a suggestion |
| Enter in search | Accept query; submit remote search |
| Enter in results | Run the displayed default action, or select in picker mode |
| Arrows / j,k | Navigate results; arrows also work while typing |
| Tab / Shift+Tab | Change focus |
| `r` / `f` / `S` | Roots and hosts / visual filters / sources |
| `s` | Sort; selecting the same column reverses direction |
| Ctrl+F | Fuzzy filter current results |
| n / N | Next / previous hit in the selected file |
| `:` | Searchable Actions popup |
| `y` | Copy menu for path or supported location formats |
| `u` | Calculate/recalculate directory disk usage |
| `H` | History; Enter views, Ctrl+R reruns, Ctrl+P pins, Ctrl+D deletes |
| Ctrl+R | Run query again |
| `p` / F2 | Toggle preview / mouse capture |
| `L` | Toggle preview line numbers |
| `?` / `q` | Searchable Help popup / quit outside text fields |
| Esc / Ctrl+C | Close interaction or cancel search |

Mouse clicks select rows, controls and columns; wheel scrolls the hovered pane.
At narrow widths preview uses the main pane when focused. Keys are configurable.

## History and cache

Confirmed searches retain query, roots, host, resolved time bounds, result metadata
and bounded snippets. Intermediate keystrokes do not create separate records.
Reopening a snapshot is offline and displays its capture time and completion state.
Rerunning creates a new record; relative dates are reevaluated. Live previews and
actions recheck the actual item.

| XDG location | Purpose |
| --- | --- |
| `$XDG_CONFIG_HOME/lazyfind/config.toml` | Preferences, roots, actions and rules |
| `$XDG_STATE_HOME/lazyfind/history.db` | SQLite search snapshots |
| `$XDG_CACHE_HOME/lazyfind/previews/` | Disposable preview cache |

Defaults use `~/.config`, `~/.local/state`, and `~/.cache`, including on macOS.
Unpinned snapshots are kept for at most 90 days and 1,000 runs; pinned snapshots
survive cleanup. Limits, stored rows and snippets are configurable. Preview cache
is capped at 256 MiB; clearing it never removes history. Deleting a snapshot prevents
late background saves from restoring that run; rerunning creates a fresh ID.

```sh
lazyfind config init
lazyfind config edit
lazyfind history list
lazyfind history show RUN_ID --json
lazyfind history rerun RUN_ID --json
lazyfind history pin RUN_ID
lazyfind cache stats
lazyfind cache clear
```

## Automation and remote search

```sh
lazyfind search 'TODO ext:go' --root ./src --json
lazyfind search invoice --root ~/Documents --save --json
lazyfind search error --host workstation --root /var/log --json
lazyfind pick --directories --print0
lazyfind doctor --json
lazyfind completion zsh
```

`search` never opens a UI; stdout contains data and stderr diagnostics. Saving is
opt-in with `--save`. JSON includes schema version, query, run status, items,
problems and truncation. Non-UTF-8 paths also carry base64 `path_bytes`. Exit codes:
0 success/empty/truncated, 1 failed/partial, 2 invalid query/config, 130 canceled.
`pick` renders on stderr and emits only the selected path on stdout (remote output
is `HOST:PATH`).

Remote queries use native OpenSSH after Enter. Paths and `~` resolve remotely;
metadata uses GNU/BSD stat. Key/agent authentication or an existing configured
shared session must work noninteractively. Existing host-key, ProxyJump, identity
and multiplexing settings are honored. No remote helper or tools are installed.
Local cancellation rejects late results; remote process-tree termination is best
effort. The roots picker offers a native SSH session for troubleshooting; returning
does not automatically rerun or make password authentication reusable.

Host discovery reads static SSH aliases and optionally `dev ssh list --json` or
`fleet hosts --list-json`. Fleet entries without SSH aliases are reported as needing
an alias rather than losing their connection settings. Discovery never connects
to hosts or runs `Match exec`.

Document hits have extracted-text locations, not original file lines or guaranteed
PDF pages. rga retains its own converter/config/cache behavior. Zoxide retains the
pathname limitations of its public CLI.

See [configuration](docs/configuration.md), [architecture](docs/architecture.md),
[verification](docs/verification.md), and [TODO.md](TODO.md).

## Development

```sh
make check
make race
make build
python3 scripts/pty_smoke.py bin/lazyfind
```

Tests use disposable fixtures and XDG directories. Real fd/rg tests skip explicitly
when tools are unavailable. Fake SSH tests exercise quoting, streams and metadata;
they do not prove real-server authentication. CI covers macOS and Linux.
