# AgentGateway capture-path decision

This spike is measured against AgentGateway v1.5.0 at image digest
`sha256:bf2f339ef326d32def2aaeb44b1b4549801293c19b89e764a4228667d97d9896` and
the generated `ext_mcp.proto` bindings from upstream commit
`fe6732474a96a0363dfb9822859af4e9bab360fa`. The wire contract is the official
`ExtMcp.CheckRequest(McpRequest)` / `CheckResponse(McpResponse)` service; the
operation context is the gateway-produced `metadata_context`, not a value
inferred by this server.

## Measured matrix

The live run is `tests/e2e_extmcp.sh`; its machine-readable output is
`testdata/expected/extmcp-spike.json`.

| Row | Request hook | Response hook | Operation context | Interpretation |
| --- | --- | --- | --- | --- |
| success echo | observed | observed | authoritative | pass-through request and response |
| tool_error | observed | observed | authoritative | gateway returned an MCP error result and still invoked the response hook |
| cancelled slow_then_cancel | observed | not observed | authoritative | v1.5.0 `notifications/cancelled` was sent while the call was active, but no terminal response hook arrived |
| missing_response disconnect | observed | not observed | authoritative | gateway killed during a distinct long-running call; no response hook |

The server retains only service names, method, request/response hook presence,
and the bounded operation context. `mcp_request` and `mcp_response` are never
copied, hashed, logged, or persisted; the live run also asserts that the
machine-readable capture has no raw-payload fields or canary bytes. The hook
API has no terminal outcome field; `outcome_authoritative` is therefore false
for every row.

The cancellation notification is a measured limitation, not a simulated
success: the live client sent `notifications/cancelled` for request id `4`,
kept AgentGateway alive, and waited for the call. AgentGateway v1.5.0 did not
produce `CheckResponse` before the bounded wait. The machine result records
`cancellation_terminal_hook: false` and marks the plan criterion unmet.

## OTLP comparison

The OTLP values below are the measured Task 9 run in
`docs/agentgateway-telemetry-matrix.md`, not assumptions from attribute names.

| Capability | ExtMCP v1.5.0 spike | OTLP v1.5.0 measured run |
| --- | --- | --- |
| Successful finish coverage | Yes when AgentGateway reaches `CheckResponse` | Yes for exported tool spans/logs |
| Error / cancel / missing-response coverage | Error response hook observed; cancel/disconnect is start-only | Error call finishes but is classified success; cancel/missing not covered |
| Authoritative operation context | Yes: gateway `metadata_context` from configured header expression | Explicit projected operation id joined by exact `(trace_id, span_id)`; delivery is client/config dependent |
| Raw payload exposure | Proto carries raw fields, but this server never persists them | Source policy removes raw args/results before export |
| Ordering and retry behavior | Ordered processor chain; response hook only after a gateway response; retry semantics not a capture guarantee | Collector fanout/batching and bridge merge wait; exact join, but export buffering affects timing |
| Configuration count | One `mcpGuardrails` processor plus one metadata expression | Gateway telemetry plus Collector fanout configuration |
| AgentGateway version coupling | Generated bindings and config are pinned to v1.5.0 | Semantic telemetry names/config are pinned to v1.5.0 |

## Decision

The decision rule is:

- choose ExtMCP primary only if operation context is authoritative and terminal
  gaps have a documented fallback;
- choose OTLP primary only if it carries explicit operation identity and exact
  pairing without client-specific heuristics;
- choose hybrid only if each path owns a distinct stated fact and deduplication
  is exact;
- if none qualifies, stop at unattributed experimental observations.

ExtMCP satisfies the operation-context condition, but this spike does not
provide a fallback that can turn a start-only disconnect or cancellation into a
terminal fact. The live cancellation notification also failed to produce a
terminal response hook, so the approved `wantFinish: true` cancellation
criterion is unmet on v1.5.0.
OTLP has exact pairing and explicit projected identity in the measured client
path, but it has the measured outcome-fidelity gap and does not cover
disconnects. A hybrid would add no exact deduplication key across the two paths
and would risk double counting. Therefore the result is **unattributed
experimental observations**; this checkpoint does not authorize Tasks 11–13.

The adapter and Core remain separate repositories. If a future checkpoint adds
a source-neutral observer contract, it should first define a bounded terminal
fallback and an exact cross-path identity before promoting either transport to
the primary capture path.

## Official references

- [AgentGateway v1.5.0 release](https://github.com/agentgateway/agentgateway/releases/tag/v1.5.0)
- [Pinned `ext_mcp.proto`](https://github.com/agentgateway/agentgateway/blob/fe6732474a96a0363dfb9822859af4e9bab360fa/crates/protos/proto/ext_mcp.proto)
- [ExtMCP guardrail configuration](https://docs.solo.io/agentgateway/standalone/latest/documentation/mcp/guardrails/about/)

## Generated binding provenance

`internal/extmcp/api/ext_mcp.pb.go` and `ext_mcp_grpc.pb.go` are copied from
the upstream API submodule at the pinned commit above. They are deliberately
limited to `ext_mcp.proto`, rather than importing the full upstream API module
(whose package also compiles unrelated resource bindings). To regenerate,
replace these two files with the generated files from that exact upstream
submodule revision and run `gofmt`, then run `make verify` and
`tests/e2e_extmcp.sh`.
