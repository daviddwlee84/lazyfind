# Parquet and structured data

Status: deferred

## Context and next implementation

The reference conversation explored Parquet, Arrow, SQL and JSONPath. v1 only
provides configurable preview/actions for these formats. Start future work with
schema inspection and explicit SQL-like predicates through DuckDB, preserving
predicate/projection pushdown. Do not stringify every cell on a keyword search.
Separate row/column locators from text/extracted-line matches. No Substrait, custom
SQL engine or sidecar full-text index is required without a concrete workload.

## Acceptance

Use the shared QuerySpec, ResultView, history and action boundaries. Include
provider-specific failure and cancellation tests and document unsupported behavior.
