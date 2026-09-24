# Git-only search

Status: deferred

## Context and next implementation

Use git ls-files -z as a candidate provider for tracked files, including
worktree/root discovery and subdirectory scoping. It must compose with existing
metadata predicates, content batches, Item IDs and history. Specify whether
submodules and deleted tracked paths appear before enabling the source.

## Acceptance

Use the shared QuerySpec, ResultView, history and action boundaries. Include
provider-specific failure and cancellation tests and document unsupported behavior.
