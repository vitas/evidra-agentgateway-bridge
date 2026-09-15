# evidra-agentgateway-bridge

Status: experimental AgentGateway observation adapter. Its JSONL output is not yet an Evidra Evidence v2 store and is not consumed by `evidra summarize`.

OTLP receiver that turns AgentGateway telemetry into normalized records of observed MCP tool
executions.

It is an **observer**. It records what a trusted boundary saw an agent do; it never records
what the agent said, and it never decides whether what the agent said was true.

```text
Agent
  │  MCP, one endpoint
  ▼
AgentGateway Virtual MCP ──── target: evidra (prescribe/report, agent-declared claims)
  │                        └─ target: kubernetes / github / database (operational work)
  │
  │  OTLP: spans carry the execution facts, access logs carry the operation id
  ▼
this bridge ── normalize ── join ── correlate ──▶ observations
```

## What it does

- Receives OTLP **logs and traces**, over HTTP (`/v1/logs`, `/v1/traces`) and gRPC (`:4317`)
- Normalizes `tools/call` telemetry into one `observation.Execution` per call
- Joins the two signals on their shared trace and span id
- Attaches each execution to an Evidra operation **only** from an explicit operation id
- Appends every execution to a JSONL artifact

## What it refuses to do

Each of these was true of the previous version of this bridge, and each is the reason it was
rewritten rather than extended.

- **It does not fabricate claims.** The old bridge turned every observed action into an
  `IngestPrescribe` call. A prescription is an *agent declaration*; an observer emitting
  declarations collapses the three-layer model the product rests on, and does it from the side
  that is supposed to be independent.
- **It does not correlate by session, time, tool name or target.** The old correlation key was
  `session|trace|method|tool|target`. Three of those five are explicitly not authoritative:
  MCP `2026-07-28` removed the protocol-level session, and matching on names or timing is a
  guess. An execution with no explicit operation id is reported as `unattributed` and stays
  that way.
- **It does not grade by HTTP status.** The old bridge computed `200 <= code < 400 → success`.
  A tool result carrying `isError: true` arrives on HTTP 200, so the failures most worth
  recording were graded as successes. Status now comes from `error.type`,
  `rpc.response.status_code` and the span status. An HTTP status is read only as evidence that
  the request *finished*, never as evidence about how the tool fared.
- **It does not carry raw tool arguments or results.** AgentGateway can export
  `gen_ai.tool.call.arguments` and `gen_ai.tool.call.result` in plaintext, and CEL can project
  arguments into access-log fields. Neither is read. `observation.Execution` has no field able
  to hold a payload, so carrying one requires someone to add a field — a visible decision.
  When a source did offer raw content, the record says `refused_raw_present_at_source` rather
  than `not_emitted_by_source`, so a privacy canary can tell "policy held" from "there was
  nothing to hold it against".

## Correlation contract

Every execution gets exactly one of:

| Value | Meaning |
|---|---|
| `correlated` | exactly one explicit, well-formed operation id was present |
| `unattributed` | no explicit operation id was present |
| `ambiguous` | conflicting ids, or an id that is not well formed |

Nothing moves an execution out of `unattributed` by reasoning about timestamps, tool names,
targets or sessions. An `ambiguous` record carries a `correlation_detail` explaining itself; a
`correlated` one is the only kind that may be attached to an operation downstream.

## Why two signals

AgentGateway has CEL projection for metrics, access logs and database fields, but **none for
span attributes** — `OtlpLoggingConfig`'s CEL is a filter, and there is no `TraceFieldsPolicy`.
So an operation id placed in W3C baggage by the client can be projected into an access-log
field, but not onto the `tools/call` span.

The execution facts live on the span; the operation id lives in the log. AgentGateway sets both
trace and span context on the exported log record (`telemetry/log.rs` calls `set_trace_context`,
and `mcp/mcp_tests.rs` asserts CEL-projected fields beside `mcp_trace` in one record), so the
join on `(trace_id, span_id)` is exact rather than heuristic.

Joining on trace id alone would not be: one trace normally contains several tool calls, and an
operation id would land on whichever execution merged first.

### The gateway configuration this depends on

```yaml
frontendPolicies:
  accessLog:
    add:
      evidra_op: request.headers['baggage'].split(',').filter(x, x.startsWith('evidra.operation.id='))[0].split('=')[1]
```

Verified against AgentGateway v1.5.0 in the CORR-0 scaffold: the projected value arrives beside
`mcp.target`, `gen_ai.tool.name` and `mcp.method.name` in one access-log record, two concurrent
operations stay on their own records, and no backend MCP server is modified.

The receiver reads the id from `evidra.operation.id`, then `evidra_op`, then
`evidra_operation_id` (`normalize.OperationIDKeys`). A whole `baggage` header is deliberately
**not** accepted: parsing a multi-member header inside the receiver would put the projection
contract in two places where it can disagree.

## Attribute names

The GenAI/MCP conventions are Development-stage, and AgentGateway emits some names of its own
beside them (`mcp.tool.name`, `mcp.error.code`, `mcp.error.message`). All of that lives in one
file, `internal/normalize/aliases.go`. Nothing downstream of `normalize` knows a non-standard
name exists.

`mcp.error.message` is deliberately not aliased to `error.type`: the conventions require
`error.type` to be low-cardinality, and laundering a free-form message into it would put an
unbounded string into a field consumers aggregate on. The message is dropped.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `EVIDRA_BRIDGE_LISTEN_ADDR` | `:4318` | OTLP/HTTP: `/v1/logs`, `/v1/traces`, `/healthz`, `/stats` |
| `EVIDRA_BRIDGE_GRPC_LISTEN_ADDR` | `:4317` | OTLP/gRPC, traces and logs |
| `EVIDRA_BRIDGE_OBSERVATIONS` | `output/observations.jsonl` | append-only JSONL artifact |
| `EVIDRA_BRIDGE_MERGE_WAIT` | `30s` | how long a single-signal record waits for the other before being emitted as partial; `0` holds partials until shutdown flushes them |
| `EVIDRA_BRIDGE_OBSERVER_ID` | `agentgateway-bridge` | written onto every execution's source |
| `EVIDRA_BRIDGE_OBSERVER_VERSION` | — | ditto |

`EVIDRA_BASE_URL` and `EVIDRA_API_KEY` are gone rather than deprecated. They pointed at the
hosted ingest API that was deleted from the product, and "accept-only mode" made a receiver
that forwarded nothing look like one that was merely unconfigured.

## Run

```bash
go run ./cmd/bridge
```

`GET /stats` is not a convenience. It is how a parity run distinguishes "the receiver saw
nothing" from "the receiver saw it and could not join it" — without it those two look identical
from outside, which is how the old correlation bug survived: the forwarder returned success and
dropped the outcome.

```bash
go run ./cmd/replay     # replays testdata/agentgateway/ as one OTLP log export
```

## Status

Done: OTLP receive (HTTP + gRPC, both signals), normalization against the semantic conventions
with an alias table, exact join, three-state correlation, JSONL sink, stats, and tests covering
the classification rules, the merge, the privacy refusal and the real gateway record shape.

Not done, in order:

1. **Evidence v2 writer.** The sink interface is where it lands. It is gated on the product
   gaining an observer provenance value: `pkg/evidence` currently offers `proxy_observed`, which
   is wrong for a record a gateway observed, and the pivot plan renames it to
   `observer_observed`. Writing gateway observations under a proxy provenance would misstate the
   source, which is the one thing this bridge must not do.
2. **Argument/result fingerprints.** An HMAC needs the raw payload and the recorder's digest
   key, and the receiver has neither by design. Duplicate detection and repeated-argument facts
   are therefore unavailable on this path until that is decided, and the record says so via
   `*_fingerprint_status` rather than implying a fingerprint that does not exist.
3. **Resource attributes.** `otlphttp.flattenLogRecords` drops `ResourceLogs.Resource`, so
   `service.name` does not reach `source.service_name`.

## Non-goals

Blocking or enforcing policy. Risk scoring. Semantic success/failure verdicts. A generic trace
backend or trace UI. N-upstream MCP federation, which is AgentGateway's job.
