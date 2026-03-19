# AgentGateway Telemetry Matrix

This bridge intentionally starts from generic telemetry that is reasonable to
emit beyond Evidra-specific use cases.

## Available Today

- log export over OTLP/HTTP
- request trace and span identifiers
- MCP method name
- tool name hints such as `gen_ai.tool.name`
- target hints such as `mcp.target`
- session hints such as `mcp.session.id`
- response status hints such as `http.status`

## Available With Configuration

- authz decision metadata if AgentGateway is configured to emit generic
  ext-authz attributes into OTEL
- deployment-specific routing labels carried through standard telemetry config

## Likely Needs Upstream Or Fork Work

- richer first-class MCP telemetry in OTEL config without custom CEL wiring
- more explicit authz decision fields as documented standard attributes
- cleaner MCP semantic examples in public AgentGateway docs

## Guardrails

This bridge should not depend on:

- raw tool arguments by default
- raw tool results by default
- Evidra-specific hashes, canonization artifacts, or scoring inputs

If richer telemetry is added later, it should remain generic and broadly useful.
