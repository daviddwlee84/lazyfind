# Changelog

## Unreleased

No unreleased changes.


## v0.1.2 - 2026-09-25

- Publish macOS/Linux amd64/arm64 binary archives, checksums and a filtered source archive.
- Add the personal Homebrew formula channel with generated Bash/Zsh completions.
- Provide `lazyfind upgrade` and read-only `--check` through verified Homebrew ownership; preserve standalone/local copies and document their external update paths.
- Verify source/module packaging independently; retain embedded resources and development history outside release payloads.
- Distribute the application under MIT.

## v0.1.1 — 2026-09-24

- Add query-condition completion, searchable Help/Actions popups, copy choices,
  configurable initial focus, match highlighting and preview line numbers.
- Add on-demand directory disk usage for selected or visible directories, with
  bounded local/SSH workers, cancellation and a session cache.
- Keep empty-query searches enabled by default, with an option to wait for input.
- Migrate history to SQLite schema v2: deleting a run prevents late saves from
  restoring it. Retry transient WAL startup contention between concurrent writers.
- Report injected release versions or Go module versions through `--version`;
  `make build` records the local tag or checkout identity.

## v0.1.0 — 2026-09-24

Baseline: `bd4bb7c`.

- Introduce fd/rg/rga/zoxide search orchestration, local and single-host SSH scopes,
  metadata filters, sorting, previews and configurable downstream actions.
- Add the keyboard/mouse TUI, scriptable CLI, XDG configuration, bounded SQLite
  search snapshots and disposable preview caching.
