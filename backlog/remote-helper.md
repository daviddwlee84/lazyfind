# Remote helper

Status: deferred

## Context and next implementation

The user explicitly deferred a remote lazyfind helper. v1 uses existing fd/rg/rga
through native OpenSSH and submits queries with Enter. Closing local SSH plus
rejecting its generation cannot prove remote descendant termination, especially
rga converters. An optional helper should define request IDs, argv transport,
streamed item/metadata events, deadlines, cancellation acknowledgments and process
group teardown. Keep host discovery separate; fleet exec buffers output and is not
a substitute for this protocol. Negotiate helper version, install only explicitly,
and retain the no-helper path. Test real SSH process cleanup on Linux/macOS before
claiming reliable remote cancellation. Password/OTP reuse needs a separately owned
connection lifecycle; one successful interactive SSH login is insufficient.

## Acceptance

Use the shared QuerySpec, ResultView, history and action boundaries. Include
provider-specific failure and cancellation tests and document unsupported behavior.
