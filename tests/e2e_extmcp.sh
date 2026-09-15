#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
out=$(mktemp -d "${TMPDIR:-/tmp}/evidra-extmcp.XXXXXX")
server_pid=''
gateway="evidra-extmcp-gateway-$$"
image='evidra-agentgateway-extmcp:spike'
cleanup() {
  rc=$?
  trap - EXIT INT TERM
  docker rm -f "$gateway" >/dev/null 2>&1 || true
  if [[ -n "$server_pid" ]]; then kill -TERM "$server_pid" >/dev/null 2>&1 || true; wait "$server_pid" || true; fi
  if (( rc == 0 )); then rm -rf "$out"; else echo "ExtMCP artifacts: $out" >&2; fi
  exit "$rc"
}
trap cleanup EXIT
on_signal() { exit "$1"; }
trap 'on_signal 130' INT
trap 'on_signal 143' TERM
for command in docker curl jq go; do command -v "$command" >/dev/null || { echo "$command is required" >&2; exit 1; }; done

go build -o "$out/extmcp-spike" "$repo_root/cmd/extmcp-spike"
docker build --tag "$image" --file "$repo_root/examples/Dockerfile.agentgateway-test" "$repo_root" >"$out/docker-build.log"
EVIDRA_EXTMCP_LISTEN_ADDR=0.0.0.0:19090 "$out/extmcp-spike" >"$out/server.jsonl" 2>"$out/server.stderr" &
server_pid=$!
sleep 0.2
docker run --detach --name "$gateway" --add-host host.docker.internal:host-gateway \
  --volume "$repo_root/examples/agentgateway-extmcp-config.yaml:/etc/agentgateway/config.yaml:ro" \
  --publish 127.0.0.1::3000 "$image" -f /etc/agentgateway/config.yaml >"$out/container.id"

gateway_port=''
for _ in $(seq 1 45); do
  gateway_port=$(docker port "$gateway" 3000/tcp 2>/dev/null | sed 's/.*://' || true)
  if [[ -n "$gateway_port" ]] && curl --silent --max-time 2 "http://127.0.0.1:$gateway_port/healthz/ready" >/dev/null 2>&1; then break; fi
  sleep 1
done
test -n "$gateway_port" || { docker logs "$gateway" >&2 || true; exit 1; }
gateway_url="http://127.0.0.1:$gateway_port/mcp"
curl --fail --silent --show-error --dump-header "$out/headers" \
  --header 'Accept: application/json, text/event-stream' --header 'Content-Type: application/json' \
  --data '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"extmcp-spike","version":"1"}}}' \
  "$gateway_url" >"$out/initialize.json"
session_id=$(awk 'BEGIN { IGNORECASE=1 } /^mcp-session-id:/ { sub(/\r$/, "", $2); print $2 }' "$out/headers" | tail -n 1)
test -n "$session_id"
curl --fail --silent --show-error --header 'Accept: application/json, text/event-stream' --header 'Content-Type: application/json' --header 'MCP-Protocol-Version: 2025-06-18' --header "Mcp-Session-Id: $session_id" \
  --data '{"jsonrpc":"2.0","method":"notifications/initialized"}' "$gateway_url" >/dev/null

call() {
  local operation=$1 payload=$2 output=$3
  curl --fail --silent --show-error --max-time 10 \
    --header 'Accept: application/json, text/event-stream' --header 'Content-Type: application/json' --header 'MCP-Protocol-Version: 2025-06-18' \
    --header "Mcp-Session-Id: $session_id" --header "x-evidra-operation-id: $operation" \
    --data "$payload" "$gateway_url" >"$output"
}
call EV-EXT-SUCCESS '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"message":"EVIDRA_PRIVATE_ARGUMENT_CANARY_EXTMCP_7b0cbb8f"}}}' "$out/success.json"
call EV-EXT-ERROR '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"definitely_missing_tool","arguments":{}}}' "$out/error.json"

# First send the MCP cancellation notification while the slow call is active.
# Then kill the real AgentGateway process during a second upstream call. The
# two operation ids make cancellation and a gateway disconnect distinct rows.
set +e
call EV-EXT-CANCEL '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"trigger-long-running-operation","arguments":{"duration":10,"steps":10}}}' "$out/cancel.json" & cancel_pid=$!
sleep 0.7
curl --silent --show-error --max-time 5 \
  --header 'Accept: application/json, text/event-stream' --header 'Content-Type: application/json' --header 'MCP-Protocol-Version: 2025-06-18' \
  --header "Mcp-Session-Id: $session_id" \
  --data '{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":4,"reason":"spike cancellation"}}' \
  "$gateway_url" >"$out/cancel-notification.json" || true
wait "$cancel_pid" || true
call EV-EXT-MISSING '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"trigger-long-running-operation","arguments":{"duration":10,"steps":10}}}' "$out/missing.json" & missing_pid=$!
sleep 0.7
docker rm -f "$gateway" >/dev/null 2>&1 || true
gateway=''
kill -TERM "$missing_pid" >/dev/null 2>&1 || true
wait "$missing_pid" || true
set -e

docker rm -f "$gateway" >/dev/null 2>&1 || true
kill -TERM "$server_pid" >/dev/null 2>&1 || true
wait "$server_pid" || true
server_pid=''
captures=$(awk '/^{/{line=$0} END{print line}' "$out/server.jsonl")
test -n "$captures"
jq -e '.captures | type == "array"' <<<"$captures" >/dev/null
jq -e 'all(.captures[]; .RawPersisted == false and (has("mcp_request") | not) and (has("mcp_response") | not))' <<<"$captures" >/dev/null
if grep -Fq 'EVIDRA_PRIVATE_ARGUMENT_CANARY_EXTMCP_7b0cbb8f' <<<"$captures"; then
  echo 'raw request canary reached machine-readable capture output' >&2
  exit 1
fi
jq -n --argjson captures "$(jq -c '.captures' <<<"$captures")" '{version:"agentgateway-v1.5.0",path:"extmcp",rows:[{name:"success echo",operation:"EV-EXT-SUCCESS",start:(([ $captures[]|select(.OperationContext=="EV-EXT-SUCCESS")|select(.RequestSeen==true) ]|length)>0),finish:(([ $captures[]|select(.OperationContext=="EV-EXT-SUCCESS" and .ResponseSeen==true) ]|length)>0),outcome_authoritative:false},{name:"tool_error",operation:"EV-EXT-ERROR",start:(([ $captures[]|select(.OperationContext=="EV-EXT-ERROR" and .RequestSeen==true) ]|length)>0),finish:(([ $captures[]|select(.OperationContext=="EV-EXT-ERROR" and .ResponseSeen==true) ]|length)>0),outcome_authoritative:false},{name:"cancelled slow_then_cancel",operation:"EV-EXT-CANCEL",start:(([ $captures[]|select(.OperationContext=="EV-EXT-CANCEL" and .RequestSeen==true) ]|length)>0),finish:(([ $captures[]|select(.OperationContext=="EV-EXT-CANCEL" and .ResponseSeen==true) ]|length)>0),outcome_authoritative:false},{name:"missing_response disconnect",operation:"EV-EXT-MISSING",start:(([ $captures[]|select(.OperationContext=="EV-EXT-MISSING" and .RequestSeen==true) ]|length)>0),finish:(([ $captures[]|select(.OperationContext=="EV-EXT-MISSING" and .ResponseSeen==true) ]|length)>0),outcome_authoritative:false}],cancellation_notification_sent:true,cancellation_terminal_hook:false,criterion_unmet:"v1.5.0 cancellation notification did not produce CheckResponse; no terminal fallback",response_hook_gaps:["CheckResponse is only called when AgentGateway receives an MCP response; disconnects are start-only.","The ExtMCP request/response API does not expose a terminal outcome field; outcome_authority=false."]}' | tee "$repo_root/testdata/expected/extmcp-spike.json"
echo 'ExtMCP E2E passed; machine-readable result written to testdata/expected/extmcp-spike.json'
