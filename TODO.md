# TODO

Deferred work for lazyfind. Design notes live in `backlog/`; active implementation
belongs in the normal code and tests. Priorities are independent of effort.

## P1

## P2
- [ ] **[L] Remote helper** — Optional remote execution supervisor for reliable cancellation, metadata streaming and live queries. → [research](backlog/remote-helper.md)
- [ ] **[L] System index sources** — Opt-in Spotlight and plocate with explicit freshness and unsupported-filter semantics. → [research](backlog/system-index.md)
- [ ] **[L] Parquet and structured data** — Schema inspection and explicit typed predicates through DuckDB. → [research](backlog/structured-data.md)
- [ ] **[M] Git-only search** — Tracked-file candidate source using git ls-files with the existing query and result models. → [research](backlog/git-only.md)

## P3
- [ ] **[L] Multi-host search** — Concurrent targets, per-host progress, partial failures and merged results. → [research](backlog/multi-host.md)

## P?

## Done

- ✅ [2026-09-25] [P3/M] Release distribution — Published v0.1.2 binary/source assets, MIT licensing, completions and a verified Homebrew upgrade/check entry point.
