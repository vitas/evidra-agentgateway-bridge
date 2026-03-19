# evidra-agentgateway-bridge

Small public bridge that converts AgentGateway OTEL logs into Evidra external
ingest calls.

## Scope

- receive OTLP/HTTP logs
- normalize AgentGateway records into observed lifecycle events
- forward typed ingest requests to Evidra

## Non-Goals

- benchmark harnesses
- scoring logic
- UI
- Kubernetes outcome correlation
