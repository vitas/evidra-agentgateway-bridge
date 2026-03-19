# evidra-agentgateway-bridge

Small public bridge that converts AgentGateway OpenTelemetry exports into
Evidra external ingest calls.

## Goal

Prove a clean minimum chain:

`kagent -> AgentGateway -> OpenTelemetry -> evidra-agentgateway-bridge -> Evidra`

This repo is the public edge only. It should be useful and shareable without
exposing benchmark harnesses, scoring internals, or private demo assets.

## Scope

- receive OTLP/HTTP traces and logs
- normalize AgentGateway telemetry into observed lifecycle events
- forward typed ingest requests to Evidra

## Non-Goals

- benchmark harnesses
- scoring logic
- UI
- Kubernetes outcome correlation

## Current Status

The MVP currently:

- accepts OTLP/HTTP log exports on `POST /v1/logs`
- accepts OTLP/HTTP trace exports on `POST /v1/traces`
- maps AgentGateway-style MCP telemetry into conservative observed action/outcome events
- forwards typed `prescribe` and `report` ingest calls into Evidra
- correlates action/outcome pairs in memory using shared session and trace context

The MVP intentionally does not:

- block or enforce policy
- scrape raw tool payloads
- infer richer semantics than the telemetry provides

## Configuration

Set:

- `EVIDRA_BRIDGE_LISTEN_ADDR`
  default `:4318`
- `EVIDRA_BASE_URL`
  Evidra API base URL, for example `http://localhost:8080`
- `EVIDRA_API_KEY`
  Bearer token for Evidra ingest

If `EVIDRA_BASE_URL` or `EVIDRA_API_KEY` is missing, the bridge still accepts
OTLP traffic but runs in accept-only mode and does not forward lifecycle events.

See [examples/bridge.env.example](examples/bridge.env.example) for a minimal
local setup and the `examples/` configs for the live AgentGateway path.

## Run

```bash
go run ./cmd/bridge
```

Endpoints:

- `POST /v1/logs`
- `POST /v1/traces`
- `GET /healthz`

## Live AgentGateway Path

The clean minimum chain proven locally is:

`AgentGateway OTLP gRPC -> OTel Collector -> bridge /v1/traces -> Evidra`

Use:

- [examples/agentgateway-config.yaml](examples/agentgateway-config.yaml)
- [examples/otel-collector-config.yaml](examples/otel-collector-config.yaml)

Notes:

- AgentGateway currently documents OTLP tracing as the primary OpenTelemetry export.
- The bridge therefore treats traces as the main live integration surface.
- The log endpoint remains available for fixture replay and fallback experiments.

## Fixture Replay

To exercise the bridge locally without a live AgentGateway export:

```bash
go run ./cmd/replay
```

Optional:

- `EVIDRA_BRIDGE_URL`
  default `http://localhost:4318`

The replay tool sends the sanitized fixture records in `testdata/agentgateway/`
as one OTLP log export to the running bridge.

## Mapping

Current taxonomy emitted to Evidra:

- `flavor = imperative`
- `evidence.kind = observed`
- `source.system = agentgateway`

Current mapping strategy:

- `tools/call` telemetry becomes `prescribe` + `report` ingest calls
- session setup traffic such as `initialize` is ignored
- correlation uses shared session/trace/tool/target fields

## Notes

- The bridge is intentionally conservative and currently treats MCP `tools/call`
  as the actionable lifecycle boundary.
- The public contract lives in this repo; Evidra internals stay private.
- Candidate upstream/doc improvements are tracked separately from the MVP.
