#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
compose_file="$repo_root/examples/compose.yaml"
expected="$repo_root/testdata/expected/otlp-stats.json"
project="evidra-otlp-$(date +%s)-$$"
output_dir=$(mktemp -d "${TMPDIR:-/tmp}/evidra-otlp.XXXXXX")
export EVIDRA_E2E_OUTPUT_DIR="$output_dir"
export DOCKER_CLIENT_TIMEOUT=120
export COMPOSE_HTTP_TIMEOUT=120

compose() {
  docker compose --project-name "$project" -f "$compose_file" "$@"
}

extract_mcp_json() {
  input=$1
  output=$2
  sed -n 's/^data: //p' "$input" | tail -n 1 >"$output"
  jq -e . "$output" >/dev/null
}

cleanup() {
  rc=$?
  if (( rc != 0 )); then
    compose logs --no-color >"$output_dir/compose.log" 2>&1 || true
    echo "E2E failed; observations and compose logs retained in $output_dir" >&2
  fi
  compose down --volumes --remove-orphans --timeout 10 >/dev/null 2>&1 || true
  if (( rc == 0 )); then
    rm -rf "$output_dir"
  fi
  exit "$rc"
}
trap cleanup EXIT INT TERM

for command in curl docker jq; do
  command -v "$command" >/dev/null || {
    echo "$command is required" >&2
    exit 1
  }
done

compose up --detach --build --wait --wait-timeout 90

gateway_addr=$(compose port agentgateway 3000)
bridge_addr=$(compose port evidra-bridge 4318)
gateway_url="http://${gateway_addr}/mcp"
bridge_url="http://${bridge_addr}"

curl --fail --silent --show-error --max-time 5 "$bridge_url/healthz" >/dev/null

operation_ok=EV-01ARZ3NDEKTSV4RRFFQ69G5FAV
operation_error=EV-01ARZ3NDEKTSV4RRFFQ69G5FAW
canary_args=EVIDRA_PRIVATE_ARGUMENT_CANARY_7b0cbb8f
canary_result=EVIDRA_PRIVATE_RESULT_CANARY_21ec0d33
headers_file="$output_dir/initialize.headers"

curl --fail --silent --show-error --max-time 10 \
  --dump-header "$headers_file" \
  --header 'Accept: application/json, text/event-stream' \
  --header 'Content-Type: application/json' \
  --header "Baggage: evidra.operation.id=$operation_ok" \
  --data '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"evidra-e2e","version":"1.0.0"}}}' \
  "$gateway_url" >"$output_dir/initialize.json"

session_id=$(awk 'BEGIN { IGNORECASE=1 } /^mcp-session-id:/ { sub(/\r$/, "", $2); print $2 }' "$headers_file" | tail -n 1)
test -n "$session_id" || {
  echo 'AgentGateway initialize response did not contain Mcp-Session-Id' >&2
  exit 1
}

curl --fail --silent --show-error --max-time 10 \
  --header 'Accept: application/json, text/event-stream' \
  --header 'Content-Type: application/json' \
  --header 'MCP-Protocol-Version: 2025-06-18' \
  --header "Mcp-Session-Id: $session_id" \
  --header "Baggage: evidra.operation.id=$operation_ok" \
  --data '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  "$gateway_url" >/dev/null

curl --fail --silent --show-error --max-time 10 \
  --header 'Accept: application/json, text/event-stream' \
  --header 'Content-Type: application/json' \
  --header 'MCP-Protocol-Version: 2025-06-18' \
  --header "Mcp-Session-Id: $session_id" \
  --header "Baggage: evidra.operation.id=$operation_ok" \
  --data "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"echo\",\"arguments\":{\"message\":\"${canary_args}_${canary_result}\"}}}" \
  "$gateway_url" >"$output_dir/success.json"
extract_mcp_json "$output_dir/success.json" "$output_dir/success.payload.json"
jq -e '.result.content | length > 0' "$output_dir/success.payload.json" >/dev/null

curl --fail --silent --show-error --max-time 10 \
  --header 'Accept: application/json, text/event-stream' \
  --header 'Content-Type: application/json' \
  --header 'MCP-Protocol-Version: 2025-06-18' \
  --header "Mcp-Session-Id: $session_id" \
  --header "Baggage: evidra.operation.id=$operation_error" \
  --data "{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"definitely_missing_tool\",\"arguments\":{\"message\":\"$canary_args\"}}}" \
  "$gateway_url" >"$output_dir/failure.json"
extract_mcp_json "$output_dir/failure.json" "$output_dir/failure.payload.json"
jq -e '.result.isError == true and (.result.content[0].text | contains("-32602"))' \
  "$output_dir/failure.payload.json" >/dev/null

# AgentGateway's OTLP SDK processors export on a batch schedule. Do not send
# SIGTERM before that bounded schedule has delivered both signals; v1.5.0 can
# otherwise spend its shutdown budget waiting on processors it has just stopped.
telemetry_ready=false
for _ in $(seq 1 30); do
  live_stats=$(curl --fail --silent --show-error --max-time 2 "$bridge_url/stats")
  if jq -e '.log_records_seen >= 4 and .spans_seen > 0' <<<"$live_stats" >/dev/null; then
    telemetry_ready=true
    break
  fi
  sleep 0.5
done
if [[ "$telemetry_ready" != true ]]; then
  echo "AgentGateway did not export both OTLP signals within 15s; bridge stats: $live_stats" >&2
  exit 1
fi

# Stop producers in order so their bounded graceful shutdowns flush through each
# downstream hop before the bridge performs its final assembly.
compose stop --timeout 10 agentgateway
compose stop --timeout 10 otel-collector
compose stop --timeout 15 evidra-bridge

actual_stats=$(compose logs --no-color evidra-bridge | sed -n 's/.*final stats: //p' | tail -n 1)
test -n "$actual_stats" || {
  echo 'bridge did not report final stats' >&2
  exit 1
}

if ! diff -u <(jq -S . "$expected") <(jq -S . <<<"$actual_stats"); then
  echo "measured bridge stats: $actual_stats" >&2
  exit 1
fi

observations="$output_dir/observations.jsonl"
test -s "$observations"
test "$(wc -l <"$observations" | tr -d ' ')" = "$(jq -r '.emitted' "$expected")"
jq -e -s '
  length == 2 and
  all(.[]; .status == "success") and
  (map(select(.tool == "definitely_missing_tool")) | length == 1) and
  all(.[]; .seen_from == ["logs", "traces"]) and
  all(.[]; .correlation == "correlated") and
  ([.[].operation_id] | sort) == [
    "EV-01ARZ3NDEKTSV4RRFFQ69G5FAV",
    "EV-01ARZ3NDEKTSV4RRFFQ69G5FAW"
  ] and
  all(.[]; .arguments_fingerprint_status == "not_emitted_by_source") and
  all(.[]; .result_fingerprint_status == "not_emitted_by_source")
' "$observations" >/dev/null

if grep -Fq "$canary_args" "$observations" || grep -Fq "$canary_result" "$observations"; then
  echo 'privacy canary reached the JSONL artifact' >&2
  exit 1
fi

echo "AgentGateway OTLP parity passed: $actual_stats"
echo 'Correlation: 2 correlated, 0 unattributed; privacy canaries absent.'
echo 'Outcome gap: the MCP failure response was isError=true, but v1.5.0 telemetry classified both spans as successful.'
