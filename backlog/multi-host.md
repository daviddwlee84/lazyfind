# Multi-host search

Status: deferred

## Context and next implementation

v1 deliberately permits one target with multiple roots per run. Extend Scope
only when simultaneous fleet search is wanted. Each target needs independent
pending, complete, failed, canceled and truncated status. Bound fan-out, preserve
successful hosts when one fails, and define result limits per target versus global.
Item identity already includes target. Sorting must not hide incomplete coverage;
actions must preserve the selected result's host.

## Acceptance

Use the shared QuerySpec, ResultView, history and action boundaries. Include
provider-specific failure and cancellation tests and document unsupported behavior.
