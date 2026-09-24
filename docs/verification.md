# Verification — 2026-09-24

## v0.1.1 patch status

Final integration passed on macOS arm64 with Go 1.27.0:

- `go test -race ./...` and `go vet ./...`: all packages passed.
- `make build VERSION=v0.1.1`: built successfully; `--version` reports `v0.1.1`.
- Documented TOML parsed successfully; retained defaults were checked.
- Expanded real PTY harness: all 18 scenario groups passed.
- `gofmt -l` and `git diff --check`: clean.

The additional PTY coverage verifies idle startup without backend processes,
Ctrl+Space conditions, exact yellow match regions, line-number toggling,
backend-free Ctrl+F, nested popups and OSC52 path:line copying, history delete
cancel/confirm without resurrection, and manual directory usage/cache/refresh/cancel.
Fixtures use isolated XDG paths; no personal host or clipboard was used.

The v0.1.0 results below describe baseline commit `bd4bb7c`.

Targeted checks completed during implementation:

- `go test -race ./internal/version`: injected/module/development version precedence.
- `go test -race ./internal/usage`: real BSD du and GNU gdu against temporary
  fixtures, hidden/ignored content, symlinks, newline paths, fake SSH, cancellation,
  concurrency, overflow and session-cache behavior.
- History/action race tests and ten repetitions of concurrent history startup,
  migration and deletion cases passed; see the [SQLite busy pitfall](../pitfalls/history-database-is-locked.md).

## v0.1.0 baseline

Environment: macOS arm64, Go 1.27.0, fd 10.3.0, ripgrep 15.1.0.

## Automated checks

- `go test -race ./...`: all packages passed; real fd/rg fixture tests executed.
- `go vet ./...`: passed.
- `go build -o bin/lazyfind .`: passed.
- Documented TOML example parsed and validated through `config show`.
- Backlog format and `git diff --check`: passed.

Coverage includes query syntax and boundaries; raw/NUL paths; binary/end-event
handling; optional-source failures; SQLite races, pins, retention and future-schema
protection; cache isolation; generation ownership; custom actions; static host
inventory; GNU/BSD remote metadata protocols; and machine-readable CLI output.

## Real PTY

`python3 scripts/pty_smoke.py bin/lazyfind` passed these ten scenarios against
isolated fixture files and XDG directories:

1. Actual fd + rg search; Enter accepts the query.
2. Arrow and Vim navigation.
3. Printable shortcuts remain text inside input.
4. Help/modal mouse isolation and source toggles.
5. Column sorting, SGR mouse press/release and wheel.
6. 80×24, 40×12 and 120×32 resizes.
7. Editor receives restored ECHO/ICANON and returns to the same query.
8. Failed external action restores a usable dashboard.
9. Quit restores terminal modes, alternate screen and mouse reporting.
10. Accepted searches persist without storing intermediate typing.

The ASCII terminal tracker is an input/state assertion aid, not a full Unicode
renderer. Separate view tests check complete graphemes and terminal cell widths.

## Limits

SSH tests run an isolated fake SSH shell; no real configured host was contacted.
Authentication, server-specific environments and remote descendant cleanup are not
claimed verified. Remote cancellation remains best effort. rga's JSON/converter
protocol is tested with a fixture; rga is not installed on this host, so actual
PDF/Office converters are unverified. Linux tests are configured in CI but the
remote CI job has not been run in this workspace.
