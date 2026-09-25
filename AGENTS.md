# lazyfind contributor guidance

This is a Go search orchestration CLI/TUI. External fd/rg/rga/zoxide remain the
search engines. Keep CLI/TUI on shared domain services and raw filenames distinct
from terminal display labels. Remote paths belong to their target, not local cwd.

- Read go.mod before using Charm examples; this project uses charm.land v2 APIs.
- Never perform I/O in TUI Update/View. Correlate every async response with its
  query, preview or dialog generation and preserve selection by identity.
- History is durable XDG state; preview cache is disposable. Opening history is
  offline. Failed persistence must not discard usable search results.
- Run go test ./..., go vet ./..., and race tests for asynchronous changes.
  Input/handoff changes also need scripts/pty_smoke.py against a built binary.
- Preserve .specstory and unrelated user work. Test with temporary fixtures/XDG;
  do not search personal hosts or modify personal configuration for smoke tests.

## Project memory

TODO.md is the single index for deferred work. Use project-knowledge-harness
init/add-todo/promote-todo commands when that skill is installed; otherwise retain
the documented P1/P2/P3/P?/Done syntax. Active entries use
`- [ ] **[M] Title** — description`, research entries use `[?/L]`, and completed
entries use `- ✅ [YYYY-MM-DD] [P2/M] Title — summary`. Link design notes under
backlog/ for large or exploratory work. Avoid parallel ROADMAP/IDEAS lists.

Capture non-obvious resolved failures under pitfalls/ with symptom-based titles,
verbatim errors, cause, fix and prevention. Repository memory is not runtime config
and must not contain credentials or personal host values.

## Binary distribution

See `docs/distribution.md`. Run GoReleaser config/snapshot checks and
`scripts/check-distribution.py` before tagging. Preserve immutable releases and
source/module exclusions. Backend setup is separate from installing this CLI.
