# Release distribution

Status: deferred

## Context and next implementation

The development entry point is go install . and local Makefile builds. Future
versioned user installations should expose version information, installation owner,
upgrade --check and an owning-manager upgrade path. Do not silently switch between
Homebrew/source/release-asset installations. Plan package signatures/checksums,
macOS/Linux architecture matrix, release notes and upgrade verification together.
Publishing artifacts is a separate action from implementing the CLI.

## Acceptance

Use the shared QuerySpec, ResultView, history and action boundaries. Include
provider-specific failure and cancellation tests and document unsupported behavior.
