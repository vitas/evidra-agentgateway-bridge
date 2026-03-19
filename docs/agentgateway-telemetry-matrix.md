# AgentGateway Telemetry Matrix

This bridge intentionally starts from generic telemetry that is reasonable to
emit beyond Evidra-specific use cases.

## Available Today

- trace export over OTLP gRPC
- export fanout through a standard OpenTelemetry Collector into OTLP/HTTP
- request trace and span identifiers
- MCP method name
- tool name hints such as `gen_ai.tool.name`
- resource name hints such as `mcp.resource.name`
- target hints such as `mcp.target`
- session hints such as `mcp.session_id`
- response status hints such as `http.status_code`

## Available With Configuration

- direct OTLP/HTTP log export if AgentGateway exposes a log pipeline in the deployment
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
