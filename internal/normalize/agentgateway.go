// Package normalize maps OTLP telemetry into observation.Execution.
//
// It is the only layer that reads attribute names. Everything downstream speaks the
// normalized record, which is what keeps a Development-stage convention - and a gateway that
// emits its own spellings beside it - from propagating through the correlation logic and into
// the evidence store.
package normalize

import (
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/vitas/evidra-agentgateway-bridge/internal/observation"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
)

// Observer identity written onto every execution, so a reader can tell a gateway-observed
// record from one an Evidra proxy observed without inferring it from the data's shape.
const (
	ObserverType    = "agentgateway_otlp"
	TransportLogs   = "otlp_logs"
	TransportTraces = "otlp_traces"
	SignalLogs      = "logs"
	SignalTraces    = "traces"

	// SpanKindClient is the gateway's outbound call to the upstream MCP server, and is the
	// span that represents the execution. SpanKindServer is the gateway's handling of the
	// inbound request that caused it. The MCP conventions model one tools/call as both,
	// parented, and both carry mcp.method.name and gen_ai.tool.name - so without the kind one
	// execution normalizes into two.
	SpanKindClient   = "client"
	SpanKindServer   = "server"
	methodToolsCall  = "tools/call"
	toolResourceType = "tool"
)

// rawPayloadKeys are the semantic-convention attributes that carry tool arguments and results
// in plaintext. They are opt-in on the source side, and this bridge never reads them.
//
// They are named here rather than simply ignored so their presence can be *reported*: a run
// where the gateway was configured to export content and the bridge still carried none is a
// privacy result, and a run where the gateway exported nothing proves nothing. See
// RefusedRaw.
var rawPayloadKeys = []string{
	"gen_ai.tool.call.arguments",
	"gen_ai.tool.call.result",
}

// callerFaultCodes are the JSON-RPC codes the MCP conventions describe as the caller's fault
// rather than the receiver's. They are still terminal failures of an execution - nothing ran -
// and are labelled as caller-side so a malformed request stays distinguishable from a tool
// that broke. See statusFrom.
var callerFaultCodes = map[string]bool{
	"-32700": true, // parse error
	"-32600": true, // invalid request
	"-32601": true, // method not found
	"-32602": true, // invalid params, also returned for an unknown tool
	"-32002": true, // resource not found
}

// FromLogRecord normalizes one OTLP log record. AgentGateway's access log is where a
// CEL-projected operation id lands, because the gateway has CEL projection for metrics,
// access logs and database fields but none for span attributes.
func FromLogRecord(record *logsv1.LogRecord) (observation.Execution, bool) {
	if record == nil {
		return observation.Execution{}, false
	}
	attrs := attributeMap(record.Attributes)
	if !isToolCall(attrs) {
		return observation.Execution{}, false
	}

	ex := observation.Execution{
		TraceID:     firstNonEmpty(hexID(record.TraceId), attrs["trace.id"]),
		SpanID:      firstNonEmpty(hexID(record.SpanId), attrs["span.id"]),
		Tool:        attr(attrs, "gen_ai.tool.name"),
		Target:      attrs["mcp.target"],
		StartedAt:   timestampFromUnixNano(record.TimeUnixNano),
		Correlation: observation.Unattributed,
		Status:      observation.StatusUnknown,
		SeenFrom:    []string{SignalLogs},
		Source:      sourceFrom(attrs, TransportLogs),
	}
	ex.ArgumentsFingerprintStatus, ex.ResultFingerprintStatus = payloadAvailability(attrs)
	ex.Status, ex.ErrorType, ex.ErrorCode = statusFrom(attrs, tracev1.Status_STATUS_CODE_UNSET, logCompleted(attrs))
	ex.Correlation, ex.OperationID, ex.CorrelationDetail = classify(attrs)
	ex.DurationMS = durationMS(attrs["duration"], attrs["duration_ms"])
	// A missing span id does not make this "not a tool call". It is reported as a normalized
	// execution with no join key, and the processor counts it separately, because "the source
	// stopped setting trace context" and "this record was a tools/list" are different failures
	// with different causes and only one of them silently loses correlation.
	return ex, ex.Tool != ""
}

// FromSpan normalizes one OTLP span. Spans carry the execution facts - timing, terminal
// status, tool, target - and the log carries the operation id; the two are merged on their
// shared trace and span id.
func FromSpan(span *tracev1.Span) (observation.Execution, bool) {
	if span == nil {
		return observation.Execution{}, false
	}
	attrs := attributeMap(span.Attributes)
	if !isToolCall(attrs) {
		return observation.Execution{}, false
	}

	spanCode := tracev1.Status_STATUS_CODE_UNSET
	if span.Status != nil {
		spanCode = span.Status.Code
	}
	// A span with an end time completed. Under the MCP conventions a successful call leaves
	// the span status UNSET, so the end time is the only positive completion signal there is.
	completed := span.EndTimeUnixNano != 0

	ex := observation.Execution{
		TraceID:      firstNonEmpty(hexID(span.TraceId), attrs["trace.id"]),
		SpanID:       firstNonEmpty(hexID(span.SpanId), attrs["span.id"]),
		ParentSpanID: hexID(span.ParentSpanId),
		SpanKind:     spanKindName(span.Kind),
		Tool:         attr(attrs, "gen_ai.tool.name"),
		Target:       attrs["mcp.target"],
		StartedAt:    timestampFromUnixNano(span.StartTimeUnixNano),
		FinishedAt:   timestampFromUnixNano(span.EndTimeUnixNano),
		Correlation:  observation.Unattributed,
		Status:       observation.StatusUnknown,
		SeenFrom:     []string{SignalTraces},
		Source:       sourceFrom(attrs, TransportTraces),
	}
	if !ex.StartedAt.IsZero() && !ex.FinishedAt.IsZero() {
		ex.DurationMS = ex.FinishedAt.Sub(ex.StartedAt).Milliseconds()
	}
	ex.ArgumentsFingerprintStatus, ex.ResultFingerprintStatus = payloadAvailability(attrs)
	ex.Status, ex.ErrorType, ex.ErrorCode = statusFrom(attrs, spanCode, completed)
	ex.Correlation, ex.OperationID, ex.CorrelationDetail = classify(attrs)
	// A missing span id does not make this "not a tool call". It is reported as a normalized
	// execution with no join key, and the processor counts it separately, because "the source
	// stopped setting trace context" and "this record was a tools/list" are different failures
	// with different causes and only one of them silently loses correlation.
	return ex, ex.Tool != ""
}

// isToolCall selects the only method this bridge normalizes. Resources, prompts and
// completions are out of scope: the plan restricts v1 to `tools/call`, and a resource read is
// not an execution of anything an agent claimed to have done.
func isToolCall(attrs map[string]string) bool {
	if attr(attrs, "mcp.method.name") != methodToolsCall {
		return false
	}
	// Some sources mark the resource type instead of, or beside, the method. Require the
	// method, and treat a contradicting resource type as "not a tool call" rather than
	// guessing which field to believe.
	if rt := attrs["mcp.resource.type"]; rt != "" && rt != toolResourceType {
		return false
	}
	return true
}

// statusFrom derives the terminal state from protocol signals only.
//
// It never reads an HTTP status. The old bridge graded `200 <= code < 400` as success, which
// is an HTTP judgement applied to an MCP call: a tool result carrying `isError: true` arrives
// on HTTP 200, so exactly the failures worth recording were graded as successes.
func statusFrom(attrs map[string]string, spanCode tracev1.Status_StatusCode, completed bool) (observation.Status, string, string) {
	errType := attr(attrs, "error.type")
	code := attr(attrs, "rpc.response.status_code")

	if strings.Contains(strings.ToLower(errType), "cancel") {
		return observation.StatusCancelled, errType, code
	}
	if errType != "" && errType != "_OTHER" {
		return observation.StatusError, errType, code
	}
	if code != "" {
		// The conventions list some JSON-RPC codes as the caller's fault and say they SHOULD
		// NOT be considered errors. That guidance is about a gateway's error rate, and it does
		// not transfer here: the agent issued a call, the server rejected it, and no tool ran.
		// Recording that as success would hide a failed action, which is the one asymmetry an
		// evidence recorder cannot afford. The distinction is preserved in the error type
		// instead, so a caller-side rejection stays tellable from a tool failure without
		// becoming a high-cardinality string.
		if callerFaultCodes[code] {
			return observation.StatusError, "caller_jsonrpc_" + strings.TrimPrefix(code, "-"), code
		}
		return observation.StatusError, firstNonEmpty(errType, "jsonrpc_"+code), code
	}
	if spanCode == tracev1.Status_STATUS_CODE_ERROR {
		return observation.StatusError, firstNonEmpty(errType, "span_error"), code
	}
	if errType == "_OTHER" {
		// The source said "an error, unclassified". Reporting success would be a lie, and
		// inventing a class would be a guess.
		return observation.StatusError, errType, code
	}
	if completed || spanCode == tracev1.Status_STATUS_CODE_OK {
		return observation.StatusSuccess, "", code
	}
	return observation.StatusUnknown, "", ""
}

// logCompleted reports whether an access-log record is itself evidence that the request
// finished. AgentGateway writes the record when the request completes and gives it a duration
// and an HTTP status, so either being present means there was a completion to record. This is
// completion evidence, not outcome evidence: a 500 here still does not make the tool call fail.
func logCompleted(attrs map[string]string) bool {
	return attrs["duration"] != "" || attrs["duration_ms"] != "" ||
		attrs["http.status"] != "" || attrs["http.status_code"] != ""
}

// payloadAvailability reports whether the source offered raw argument/result content. The
// bridge carries neither, so the answer is either "the source emitted nothing" or "the source
// emitted it and policy declined" - and the second is the one a privacy canary asserts on.
func payloadAvailability(attrs map[string]string) (observation.Availability, observation.Availability) {
	args, result := observation.NotEmitted, observation.NotEmitted
	for _, key := range rawPayloadKeys {
		if attrs[key] == "" {
			continue
		}
		if strings.Contains(key, "arguments") {
			args = observation.RefusedRaw
		} else {
			result = observation.RefusedRaw
		}
	}
	return args, result
}

// classify reads every operation id the signal carries and lets observation.ClassifyOperation
// decide. Conflicting ids stay conflicting; nothing here picks a winner by preference order.
func classify(attrs map[string]string) (observation.Correlation, string, string) {
	return observation.ClassifyOperation(operationID(attrs)...)
}

func sourceFrom(attrs map[string]string, transport string) observation.Source {
	return observation.Source{
		ObserverType:     ObserverType,
		Transport:        transport,
		ServiceName:      attrs["service.name"],
		ProtocolVersion:  attr(attrs, "mcp.protocol.version"),
		NetworkTransport: attrs["network.transport"],
		SessionID:        attr(attrs, "mcp.session.id"),
	}
}

func durationMS(values ...string) int64 {
	for _, v := range values {
		v = strings.TrimSpace(strings.TrimSuffix(v, "ms"))
		if v == "" {
			continue
		}
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return int64(f)
		}
	}
	return 0
}

func attributeMap(attributes []*commonv1.KeyValue) map[string]string {
	values := make(map[string]string, len(attributes))
	for _, attribute := range attributes {
		if attribute == nil || attribute.Value == nil {
			continue
		}
		values[attribute.Key] = anyValueString(attribute.Value)
	}
	return values
}

func anyValueString(value *commonv1.AnyValue) string {
	switch v := value.Value.(type) {
	case *commonv1.AnyValue_StringValue:
		return v.StringValue
	case *commonv1.AnyValue_BoolValue:
		return strconv.FormatBool(v.BoolValue)
	case *commonv1.AnyValue_IntValue:
		return strconv.FormatInt(v.IntValue, 10)
	case *commonv1.AnyValue_DoubleValue:
		return strconv.FormatFloat(v.DoubleValue, 'g', -1, 64)
	default:
		return ""
	}
}

func hexID(id []byte) string {
	if len(id) == 0 {
		return ""
	}
	return hex.EncodeToString(id)
}

func timestampFromUnixNano(unixNano uint64) time.Time {
	if unixNano == 0 {
		return time.Time{}
	}
	return time.Unix(0, int64(unixNano)).UTC()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// spanKindName renders the OTLP span kind. The MCP conventions emit a CLIENT span and a
// SERVER span for one tools/call, so the kind is what tells them apart; an empty kind means
// the source did not say, which is recorded as such rather than defaulted to CLIENT.
func spanKindName(kind tracev1.Span_SpanKind) string {
	switch kind {
	case tracev1.Span_SPAN_KIND_CLIENT:
		return SpanKindClient
	case tracev1.Span_SPAN_KIND_SERVER:
		return SpanKindServer
	case tracev1.Span_SPAN_KIND_INTERNAL:
		return "internal"
	case tracev1.Span_SPAN_KIND_PRODUCER:
		return "producer"
	case tracev1.Span_SPAN_KIND_CONSUMER:
		return "consumer"
	default:
		return ""
	}
}
