# Architecture

CLI, TUI and history reruns share `domain.QuerySpec` and the search service.
`BaseFilters` preserves the configured/form baseline when inline qualifiers are
removed. Each Run resolves roots and relative dates, streams immutable item upserts,
and finishes with a snapshot. Items use target + raw absolute path identities.

fd owns traversal and ignore semantics. Metadata conditions filter candidates before
content reads. NUL paths become bounded argv batches for rg/rga JSON output; paths
are never piped as searchable text. The JSON parser supports text/base64, oversized
records, partial errors and binary detection. Matches commit at the file end event.
Per-file match limits follow rg's binary detection boundary and do not classify
unread file regions. rga hits retain extracted-line semantics.

Transport owns the single implicit shell boundary: individually quoted remote
arguments through OpenSSH. Local background process groups are canceled. Remote
cancellation is best effort; a supervisor protocol is deferred to the helper backlog.
Interactive children use native terminal handoff and SSH PTY allocation.

Actions and previews classify selected items asynchronously. Preview rechecks
metadata, sanitizes controls, limits output, and uses a disposable cache. Hosts are
static aliases from config or optional inventory CLIs; no credentials or private
provider databases are imported. Zoxide's public CLI supplies frequent directories.

Bubble Tea owns state; query, preview and dialog generations reject stale replies.
I/O runs outside Update/View. Pure layout geometry also determines mouse targets.
Text fields own printable keys. Sorting and fuzzy filtering preserve selected identity.
Qualifier completion uses the shared query definitions. Help and Actions use
searchable popups; preview match spans and optional line numbers are display state.

Directory usage is a separate on-demand service. A bounded worker pool runs
`env LC_ALL=C du -skP .` with the selected raw path as cwd through the same transport.
Numeric KiB totals are checked before conversion to allocated bytes. Partial,
failed, canceled and timed-out measurements stay distinct from complete results.
The TUI owns cancellation and reply generations; only complete results enter its
session cache, keyed by target and raw path. Usage never changes file-size filters
or persisted search snapshots.

Only confirmed runs persist. SQLite updates a partial run to its terminal snapshot,
rejects older writes and terminal-to-running regressions, and preserves pin state.
History viewing is offline; reruns create new linked IDs and re-evaluate relative
dates. Schema v2 adds deletion tombstones, so a late partial/final save cannot
recreate a deleted run. Migration takes a write lock before rechecking the schema;
WAL activation retries only SQLite busy errors with a bounded context-aware wait.
Unknown future schemas are rejected. Pruning history and
clearing cache operate on different XDG trees. Persistence errors leave live results usable.

The root main package supports `go install .`. Version resolution uses an injected
build identity, then the Go main-module version, then `dev`; runtime startup never
calls Git. The Makefile supplies a local tag or commit identity for checkout builds.

Tests use isolated state, actual fd/rg,
fake SSH and real PTY input. Fake SSH does not verify real-host authentication or
remote process-tree shutdown. The rga protocol fixture does not certify installed
third-party converters. Linux runtime is covered by CI, not by cross-compilation.
