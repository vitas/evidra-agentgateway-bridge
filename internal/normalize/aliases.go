package normalize

import "strings"

// This file is the only place in the bridge that knows a non-standard attribute name.
//
// AgentGateway emits some attributes that are not in the OTel GenAI/MCP semantic
// conventions - `mcp.tool.name`, `mcp.error.code`, `mcp.error.message`, the legacy
// `gen_ai.usage.prompt_tokens` spelling - and older builds spelled `mcp.session_id` with an
// underscore. Everything downstream of normalize speaks semconv only, so a gateway that
// renames a field is a change to this table and not to the correlation logic, the sinks, or
// the evidence writer.
//
// The conventions themselves are Development-stage: `mcp.*` and `gen_ai.*` are not Stable,
// `rpc.response.status_code` is a Release Candidate, and only `error.type` and `network.*`
// are Stable. So the names below are expected to move. Isolating them is the mitigation;
// pretending they are fixed is not available.

// semconvOf maps a canonical semantic-convention key to the aliases that may carry it, in
// preference order. The canonical key is tried first by the caller, so it does not appear
// here unless it also has a legacy spelling worth listing.
var semconvOf = map[string][]string{
	"gen_ai.tool.name":           {"mcp.tool.name", "mcp.resource.name"},
	"mcp.method.name":            {"mcp.method"},
	"mcp.session.id":             {"mcp.session_id"},
	"rpc.response.status_code":   {"mcp.error.code"},
	"gen_ai.usage.input_tokens":  {"gen_ai.usage.prompt_tokens"},
	"gen_ai.usage.output_tokens": {"gen_ai.usage.completion_tokens"},
	"gen_ai.request.model":       {"gen_ai.model"},
	"mcp.protocol.version":       {"mcp.protocol_version"},
	"gen_ai.operation.name":      {"gen_ai.operation"},
	"gen_ai.provider.name":       {"gen_ai.system"},
}

// Deliberately NOT aliased, and the reason matters.
//
// `mcp.error.message` is a human-readable message. The semconv `error.type` is required to
// be low-cardinality and predictable, and mapping a free-form message onto it would put an
// unbounded string into a field consumers aggregate on. The message is dropped rather than
// laundered. If an error class is needed, it comes from `error.type` where the source emits
// it, or from `rpc.response.status_code`.
//
// `http.status` / `http.status_code` are not aliased to anything MCP-shaped either. The old
// bridge derived an execution verdict from `200 <= code < 400`, which is an HTTP judgement
// applied to an MCP call: a `tools/call` that returns `isError: true` rides on HTTP 200, so
// the most interesting failures were graded as successes. MCP status comes from the JSON-RPC
// error object and the tool result's isError flag.

// attr looks a canonical key up in an attribute map, falling back through its aliases.
// It returns the empty string when neither the canonical key nor any alias is present, and
// callers must treat that as "the source declared nothing" rather than as a value.
func attr(attrs map[string]string, canonical string) string {
	if v, ok := attrs[canonical]; ok && v != "" {
		return v
	}
	for _, alias := range semconvOf[canonical] {
		if v, ok := attrs[alias]; ok && v != "" {
			return v
		}
	}
	return ""
}

// OperationIDKeys are the attribute names that may carry an explicit Evidra operation id, in
// preference order.
//
// The first is the CEL-projected access-log field, which is how the id reaches gateway
// telemetry today: AgentGateway has CEL projection for metrics, access logs and database
// fields but none for span attributes, so the id is projected into a log field and joined to
// the span by trace and span id. The name must match the gateway configuration; the CORR-0
// scaffold uses `evidra_op`.
//
// The second is what a future gateway (or a span-attribute projection, if one is added) would
// emit directly. Listing it costs nothing and means the receiver does not have to change when
// the gateway does.
//
// A whole `baggage` header is deliberately not in this list. Recovering one member by parsing
// a multi-member header inside the receiver would mean the projection contract lives in two
// places, and the plan forbids exporting the entire baggage merely to recover one value.
var OperationIDKeys = []string{
	"evidra.operation.id",
	"evidra_op",
	"evidra_operation_id",
}

// operationID extracts every distinct operation id a signal carries. It returns all of them
// rather than the first, because two different ids on one record is exactly the conflict that
// must surface as ambiguous instead of being resolved by preference order.
func operationID(attrs map[string]string) []string {
	var found []string
	seen := map[string]bool{}
	for _, key := range OperationIDKeys {
		for _, v := range strings.Split(attrs[key], ",") {
			v = strings.TrimSpace(v)
			if v == "" || seen[v] {
				continue
			}
			seen[v] = true
			found = append(found, v)
		}
	}
	return found
}
