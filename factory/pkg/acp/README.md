# pkg/acp

A client for the [Agent Client Protocol](https://agentclientprotocol.com) —
JSON-RPC 2.0 over newline-delimited JSON on an agent process's stdio.

## Provenance

`client.go`, `types.go`, `jsonrpc.go` and `jsonrpc_test.go` were copied
verbatim from `examples/agentclientprotocol/pkg/acp/` in
[kubernetes-sigs/agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox),
Apache 2.0, and keep their original copyright headers.

They are copied rather than imported because the upstream lives in an
`examples/` directory: it is not a released package, carries no API
stability promise, and taking a module dependency on another repository's
example is not a contract worth depending on. There is no official Go ACP
library — the official implementations are Rust and TypeScript — which is
presumably why upstream hand-rolled this one.

Copied rather than rewritten because the protocol layer is a wire mapping:
a reimplementation would be the same structs with worse comments, and would
throw away the one thing this code has that a fresh one would not — it is
known to work against `gemini --acp`.

## Local changes

None yet. This is our copy and may be edited freely; there is no upstream
sync process. Record any divergence here so the next reader knows this is
no longer a straight copy.

## Layering

This package speaks the protocol and nothing else. Session lifecycle,
process supervision, transcript persistence and the HTTP surface live in
`pkg/acpd`, which uses this.
