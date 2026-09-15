# Plan: AgentGateway upstream observability fix

## Objective

Prepare and submit an upstream pull request to AgentGateway that closes the
capture gaps measured against v1.5.0, then rerun the Evidra bridge parity matrix
before considering any promotion to a Core observer contract.

The bridge remains a separate experimental repository throughout this work.
No AgentGateway observation is promoted to Evidence v2 by this plan.

## Measured baseline

The current bridge spike is reproducible with AgentGateway v1.5.0:

- ExtMCP receives request and response hooks for success and tool-error calls.
- ExtMCP receives a request hook but no terminal response hook after a real
  `notifications/cancelled` request.
- A disconnected request is also start-only.
- The ExtMCP protocol exposes authoritative `metadata_context`, but no
  terminal outcome field.
- OTLP preserves operation identity in the measured client path, but an MCP
  `isError=true` response is exported with source status `success`.

Evidence and exact commands are recorded in:

- `docs/agentgateway-capture-decision.md`
- `docs/agentgateway-telemetry-matrix.md`
- `tests/e2e_extmcp.sh`
- `tests/e2e_otlp.sh`

## PR boundary

Start by checking whether the two defects share a common AgentGateway code
path. Submit one focused PR only if ownership and tests remain clear. Otherwise
submit two PRs:

### PR A: terminal lifecycle hooks

- Emit a terminal ExtMCP response/termination hook for cancellation.
- Emit an explicit terminal reason for disconnect or otherwise document why a
  terminal fact cannot be produced.
- Preserve the authoritative operation context from `metadata_context`.
- Define ordering and retry behavior for request, cancellation, response and
  disconnect paths.

### PR B: outcome propagation

- Map MCP `isError=true` to an explicit OTLP error outcome/status.
- Preserve the operation identity used by the access-log and span projection.
- Add tests proving success and tool-error calls cannot collapse to the same
  source status.

Do not add Evidra-specific persistence, Evidence v2 schemas, or product policy
to AgentGateway. The upstream change should expose facts; the bridge remains
responsible for bounded observation and privacy filtering.

## Phase 1: reproduce upstream behavior

1. Fork or create a local AgentGateway branch at the exact v1.5.0 baseline.
2. Import the bridge's four lifecycle cases without Evidra-specific heuristics:
   success, tool error, cancellation, and disconnect.
3. Add failing upstream tests that demonstrate:
   - cancellation has no terminal hook;
   - disconnect behavior is explicit and bounded;
   - MCP error outcome is lost in OTLP;
   - operation context is preserved end to end.
4. Record the exact AgentGateway commit, test command, runtime image and
   protocol inputs used by the tests.

Expected RED result: the tests fail against the unpatched upstream baseline.

## Phase 2: implement the smallest upstream fix

1. Trace the request/response, cancellation and disconnect lifecycle to the
   authoritative AgentGateway components.
2. Implement terminal signaling without guessing from timing, tool names,
   trace IDs or payload contents.
3. Make cancellation and disconnect reasons explicit and serializable.
4. Map MCP error results to the documented OTLP status/error fields without
   exposing raw arguments or results.
5. Preserve backward compatibility for clients that do not send operation
   context.
6. Add comments only where protocol behavior is non-obvious; avoid broad
   refactors.

Expected GREEN result: upstream tests pass for all four lifecycle cases and
the OTLP outcome test distinguishes success from MCP error.

## Phase 3: upstream verification

Run the upstream project's documented checks plus:

```text
formatting and lint
unit tests for touched packages
race tests where supported
the four lifecycle regression tests
the OTLP outcome regression test
container/config validation for the pinned test image
```

Record failures caused by environment or unavailable credentials separately
from code failures. Do not claim the PR is ready while a required check is
unverified.

## Phase 4: bridge compatibility run

After the upstream fix is available as a commit or release candidate:

1. Pin the bridge test image to that exact AgentGateway commit or digest.
2. Run `tests/e2e_extmcp.sh` and `tests/e2e_otlp.sh` twice from clean compose
   projects.
3. Require these outcomes:
   - success: request and terminal response observed;
   - tool error: request and terminal response observed with error outcome;
   - cancellation: request and terminal termination observed;
   - disconnect: either a documented terminal reason or an explicit bounded
     `unknown` state, never an inferred success;
   - operation IDs remain authoritative and conflicting IDs remain ambiguous;
   - no raw arguments/results or privacy canaries appear in JSONL;
   - no duplicate execution is emitted across OTLP and ExtMCP paths.
4. Update the decision document only from measured output.

## Acceptance gate

Do not start the Core observer-store work until all are true:

- upstream regression tests are merged or available in a pinned release
  candidate;
- cancellation has a terminal hook or an explicitly specified terminal
  fallback;
- MCP error outcome is authoritative in the chosen transport;
- operation identity is exact and non-heuristic;
- the bridge parity matrix passes twice from clean environments;
- the decision table has no unresolved double-counting path;
- privacy and durability claims remain limited to what the sink actually
  guarantees.

If any condition fails, keep the integration separate and report only
unattributed experimental observations.

## Deliverables

- One or two focused AgentGateway PRs, depending on ownership boundaries.
- Upstream regression tests and verification output.
- Updated pinned bridge test image/configuration.
- Updated `docs/agentgateway-capture-decision.md` and telemetry matrix based
  on measured results.
- A short release-note entry describing the new terminal/outcome semantics.

## Out of scope

- Moving the bridge into the Core repository.
- Creating an Evidence v2 observer store.
- Treating OTLP or ExtMCP as production capture without the acceptance gate.
- Adding raw payload capture, credentials, or product-specific policy upstream.
- Publishing, tagging, or releasing AgentGateway without explicit approval.
