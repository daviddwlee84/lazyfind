# System index sources

Status: deferred

## Context and next implementation

The first release deliberately ships fd/rg/rga plus optional zoxide. Add
Spotlight on macOS and plocate on Linux as explicitly enabled retrieval sources.
Indexed existence/freshness differs from live traversal and fd ignore semantics.
Define supported predicates and verify selected results without treating an old
index as complete. Preserve source provenance, deduplicate by target/path, and use
existing action/history services. Do not build a new background index in lazyfind.

## Acceptance

Use the shared QuerySpec, ResultView, history and action boundaries. Include
provider-specific failure and cancellation tests and document unsupported behavior.
