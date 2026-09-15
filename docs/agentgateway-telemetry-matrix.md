# AgentGateway Telemetry Matrix

This matrix records the measured `examples/compose.yaml` path. It does not infer
fields from documentation or from AgentGateway's stdout access log.

## Measured on AgentGateway v1.5.0

- AgentGateway exports traces and access logs directly to the Collector over
  OTLP/gRPC.
- The Collector fans both signals out to the bridge over the committed
  `/v1/traces` and `/v1/logs` OTLP/HTTP routes.
- Six MCP requests produce 6 log records and 12 spans. The bridge ignores the
  six non-tool-call signals from initialize and initialized, suppresses the
  four server/parent tool spans, and emits exactly 4 merged execution records.
- Both tool executions carry trace id, span id, parent span id, MCP method,
  `gen_ai.tool.name`, `mcp.target`, and the diagnostic MCP session id.
- `frontendPolicies.accessLog.add.evidra_op` projects exact comma-delimited
  baggage members. Two valid ids correlate, an unrelated substring remains
  unattributed, and conflicting exact ids remain ambiguous: `correlated=2`,
  `unattributed=1`, `ambiguous=1`.
- Raw arguments and results are explicitly removed from both telemetry
  policies. Two request/result canaries are absent from JSONL, and both
  fingerprint availability fields read `not_emitted_by_source`.

## Measured gap

The test's unknown-tool call returns a valid MCP tool result with `isError:
true` and error code `-32602` in its text. AgentGateway v1.5.0 exports no
`mcp.error.*`, `error.type`, or error span status for that response. The bridge
therefore emits both records with source-reported status `success`.

Recovering that outcome from raw result content would violate the privacy
configuration. Until AgentGateway exports a bounded terminal outcome attribute,
this transport has execution and correlation parity but not outcome parity.

## Not exercised by this parity run

- direct AgentGateway-to-bridge OTLP/HTTP (the committed topology intentionally
  measures Collector fanout)
- authz decision metadata
- resource and prompt telemetry, which the bridge intentionally ignores
- raw tool arguments or results

## Guardrails

This bridge should not depend on:

- raw tool arguments by default
- raw tool results by default
- Evidra-specific hashes, canonization artifacts, or scoring inputs

If richer telemetry is added later, terminal outcome should be a bounded,
generic attribute rather than raw result content.

## Reproduce

```bash
EVIDRA_E2E_OUTPUT_DIR=/tmp/evidra-compose-config docker compose -f examples/compose.yaml config
./tests/e2e_otlp.sh
```

The expected full counter object is committed at
`testdata/expected/otlp-stats.json`; any signal-count, merge, correlation, or
buffering drift fails the run.
