package normalize

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/vitas/evidra-agentgateway-bridge/internal/observation"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
)

// The operation id shape Evidra issues: EV- plus a 26 character Crockford base32 ULID. Taken
// from a real recorder directory so the tests are not validating a pattern the author invented.
const realOperationID = "EV-01M2FAS00J36FTV4EG5EJC1D7S"

func str(k, v string) *commonv1.KeyValue {
	return &commonv1.KeyValue{Key: k, Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: v}}}
}

func id(t *testing.T, hexStr string) []byte {
	t.Helper()
	b, err := hex.DecodeString(hexStr)
	if err != nil {
		t.Fatalf("bad test id %q: %v", hexStr, err)
	}
	return b
}

const (
	testTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	testSpanID  = "00f067aa0ba902b7"
)

// gatewayAccessLogFields reproduces the attribute set of a real AgentGateway access-log record
// for a successful tools/call, captured from AgentGateway v1.5.0 running the CORR-0 scaffold.
// It is a fixture rather than a construction: the point of the test is that the receiver reads
// what the gateway actually emits, including the field names the gateway invented that are not
// in the semantic conventions.
func gatewayAccessLogFields(operationID string) []*commonv1.KeyValue {
	fields := []*commonv1.KeyValue{
		str("protocol", "mcp"),
		str("http.method", "POST"),
		str("http.path", "/mcp"),
		str("http.status", "200"),
		str("mcp.method.name", "tools/call"),
		str("mcp.target", "alpha"),
		str("mcp.resource.type", "tool"),
		str("gen_ai.tool.name", "probe_alpha_act"),
		str("mcp.session.id", "4061ad7e-b9e9-4cb4-a794-f470709db803"),
		str("duration", "6"),
		str("trace.id", testTraceID),
		str("span.id", testSpanID),
		str("evidra_baggage_raw", "evidra.operation.id="+operationID+",noise=1"),
		str("evidra_op", operationID),
	}
	return fields
}

func TestFromLogRecordReadsTheRealGatewayAccessLog(t *testing.T) {
	record := &logsv1.LogRecord{
		TraceId:      id(t, testTraceID),
		SpanId:       id(t, testSpanID),
		TimeUnixNano: 1757872938000000000,
		Attributes:   gatewayAccessLogFields(realOperationID),
	}

	ex, ok := FromLogRecord(record)
	if !ok {
		t.Fatal("a real gateway tools/call access-log record was not normalized")
	}
	if ex.TraceID != testTraceID || ex.SpanID != testSpanID {
		t.Errorf("identity = %s/%s, want %s/%s", ex.TraceID, ex.SpanID, testTraceID, testSpanID)
	}
	if ex.Tool != "probe_alpha_act" {
		t.Errorf("tool = %q", ex.Tool)
	}
	if ex.Target != "alpha" {
		t.Errorf("target = %q", ex.Target)
	}
	if ex.Correlation != observation.Correlated || ex.OperationID != realOperationID {
		t.Errorf("correlation = %s/%q, want correlated/%s", ex.Correlation, ex.OperationID, realOperationID)
	}
	// The gateway reports http.status=200 and no error.type. The old bridge graded this from
	// the HTTP range; the new one must reach success from the absence of a protocol error,
	// not from a transport code that means nothing about an MCP tool result.
	if ex.Status != observation.StatusSuccess {
		t.Errorf("status = %s, want success", ex.Status)
	}
	if ex.DurationMS != 6 {
		t.Errorf("duration = %d, want 6", ex.DurationMS)
	}
	if ex.Source.ObserverType != ObserverType || ex.Source.Transport != TransportLogs {
		t.Errorf("source = %+v", ex.Source)
	}
	// Session id is carried, but as the diagnostic it is - never as the correlation key.
	if ex.Source.SessionID == "" {
		t.Error("session id should be carried as diagnostic metadata")
	}
	if ex.OperationID == ex.Source.SessionID {
		t.Error("operation id must not be derived from the session id")
	}
}

func TestFromLogRecordIgnoresNonToolCalls(t *testing.T) {
	for _, method := range []string{"initialize", "tools/list", "resources/read", "prompts/get", ""} {
		fields := gatewayAccessLogFields(realOperationID)
		for i, f := range fields {
			if f.Key == "mcp.method.name" {
				fields[i] = str("mcp.method.name", method)
			}
		}
		if _, ok := FromLogRecord(&logsv1.LogRecord{Attributes: fields, TraceId: id(t, testTraceID), SpanId: id(t, testSpanID)}); ok {
			t.Errorf("method %q was normalized as an execution", method)
		}
	}
}

func TestFromLogRecordRejectsContradictingResourceType(t *testing.T) {
	fields := append(gatewayAccessLogFields(realOperationID), str("mcp.resource.type", "resource"))
	// Overriding the earlier value: the last one wins in attributeMap, which is what makes
	// this a contradiction rather than a duplicate.
	if _, ok := FromLogRecord(&logsv1.LogRecord{Attributes: fields, TraceId: id(t, testTraceID), SpanId: id(t, testSpanID)}); ok {
		t.Error("a record whose resource type contradicts tools/call was accepted")
	}
}

func TestStatusComesFromProtocolSignalsNotHTTP(t *testing.T) {
	cases := []struct {
		name   string
		attrs  []*commonv1.KeyValue
		span   tracev1.Status_StatusCode
		want   observation.Status
		errTyp string
	}{
		{
			name: "tool result isError maps to tool_error",
			attrs: []*commonv1.KeyValue{
				str("mcp.method.name", "tools/call"), str("gen_ai.tool.name", "t"),
				str("error.type", "tool_error"), str("http.status", "200"),
			},
			want: observation.StatusError, errTyp: "tool_error",
		},
		{
			name: "jsonrpc error code is an error",
			attrs: []*commonv1.KeyValue{
				str("mcp.method.name", "tools/call"), str("gen_ai.tool.name", "t"),
				str("rpc.response.status_code", "-32603"),
			},
			want: observation.StatusError, errTyp: "jsonrpc_-32603",
		},
		{
			// The conventions call this the caller's fault and say it SHOULD NOT count as an
			// error - about the gateway's error rate. It is still a terminal failure of this
			// execution: the agent called a tool and nothing ran. Grading it success would
			// hide a failed action.
			name: "invalid params is caller-side, and still not a success",
			attrs: []*commonv1.KeyValue{
				str("mcp.method.name", "tools/call"), str("gen_ai.tool.name", "t"),
				str("rpc.response.status_code", "-32602"),
			},
			want: observation.StatusError, errTyp: "caller_jsonrpc_32602",
		},
		{
			name: "cancellation is its own status",
			attrs: []*commonv1.KeyValue{
				str("mcp.method.name", "tools/call"), str("gen_ai.tool.name", "t"),
				str("error.type", "cancelled_by_client"),
			},
			want: observation.StatusCancelled,
		},
		{
			name: "span error with no attributes",
			attrs: []*commonv1.KeyValue{
				str("mcp.method.name", "tools/call"), str("gen_ai.tool.name", "t"),
			},
			span: tracev1.Status_STATUS_CODE_ERROR,
			want: observation.StatusError,
		},
		{
			// The call completed and no protocol signal reported an error. The transport
			// status is not consulted for the outcome - only, elsewhere, for whether the
			// request finished at all.
			name: "http 500 alone does not make the tool call fail",
			attrs: []*commonv1.KeyValue{
				str("mcp.method.name", "tools/call"), str("gen_ai.tool.name", "t"),
				str("http.status", "500"),
			},
			want: observation.StatusSuccess,
		},
		{
			name: "_OTHER is an error, not a success",
			attrs: []*commonv1.KeyValue{
				str("mcp.method.name", "tools/call"), str("gen_ai.tool.name", "t"),
				str("error.type", "_OTHER"),
			},
			want: observation.StatusError, errTyp: "_OTHER",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			span := &tracev1.Span{
				TraceId: id(t, testTraceID), SpanId: id(t, testSpanID),
				// A completed call. Under the MCP conventions success leaves the span status
				// UNSET, so the end time is the only positive completion signal available.
				EndTimeUnixNano: 1757872938000000000,
				Status:          &tracev1.Status{Code: tc.span},
				Attributes:      tc.attrs,
			}
			ex, ok := FromSpan(span)
			if !ok {
				t.Fatal("span was not normalized")
			}
			if ex.Status != tc.want {
				t.Errorf("status = %s, want %s", ex.Status, tc.want)
			}
			if tc.errTyp != "" && ex.ErrorType != tc.errTyp {
				t.Errorf("error_type = %q, want %q", ex.ErrorType, tc.errTyp)
			}
		})
	}
}

func TestAliasTableReadsGatewaySpellings(t *testing.T) {
	// AgentGateway emits mcp.tool.name and mcp.error.code, neither of which is in the
	// semantic conventions. The alias table is the only place that knows this.
	span := &tracev1.Span{
		TraceId: id(t, testTraceID), SpanId: id(t, testSpanID),
		Attributes: []*commonv1.KeyValue{
			str("mcp.method.name", "tools/call"),
			str("mcp.tool.name", "gateway_spelled_tool"),
			str("mcp.error.code", "-32603"),
		},
	}
	ex, ok := FromSpan(span)
	if !ok {
		t.Fatal("span was not normalized")
	}
	if ex.Tool != "gateway_spelled_tool" {
		t.Errorf("tool = %q, want the gateway spelling resolved through the alias table", ex.Tool)
	}
	if ex.Status != observation.StatusError || ex.ErrorCode != "-32603" {
		t.Errorf("status = %s code = %q, want error/-32603", ex.Status, ex.ErrorCode)
	}
}

func TestSemconvNameWinsOverAlias(t *testing.T) {
	span := &tracev1.Span{
		TraceId: id(t, testTraceID), SpanId: id(t, testSpanID),
		Attributes: []*commonv1.KeyValue{
			str("mcp.method.name", "tools/call"),
			str("gen_ai.tool.name", "canonical"),
			str("mcp.tool.name", "legacy"),
		},
	}
	ex, _ := FromSpan(span)
	if ex.Tool != "canonical" {
		t.Errorf("tool = %q, want the semconv name to win over its alias", ex.Tool)
	}
}

// TestRawPayloadsAreRefusedAndReported is the privacy canary. AgentGateway can be configured
// to export gen_ai.tool.call.arguments and gen_ai.tool.call.result in plaintext, and CEL can
// project tool arguments into access-log fields. The receiver must carry neither, and must say
// that it declined rather than reporting an absence that looks like the source emitted nothing.
func TestRawPayloadsAreRefusedAndReported(t *testing.T) {
	const secret = "password=hunter2-and-a-real-secret"
	span := &tracev1.Span{
		TraceId: id(t, testTraceID), SpanId: id(t, testSpanID),
		Attributes: []*commonv1.KeyValue{
			str("mcp.method.name", "tools/call"),
			str("gen_ai.tool.name", "t"),
			str("gen_ai.tool.call.arguments", `{"credentials":"`+secret+`"}`),
			str("gen_ai.tool.call.result", `{"token":"`+secret+`"}`),
		},
	}
	ex, ok := FromSpan(span)
	if !ok {
		t.Fatal("span was not normalized")
	}
	if ex.ArgumentsFingerprintStatus != observation.RefusedRaw || ex.ResultFingerprintStatus != observation.RefusedRaw {
		t.Errorf("availability = args:%s result:%s, want both refused_raw_present_at_source",
			ex.ArgumentsFingerprintStatus, ex.ResultFingerprintStatus)
	}
	if ex.ArgumentsFingerprint != "" || ex.ResultFingerprint != "" {
		t.Error("a fingerprint was carried without a writer computing one")
	}

	raw, err := json.Marshal(ex)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), secret) {
		t.Errorf("the raw payload reached the serialized record:\n%s", raw)
	}
	if strings.Contains(string(raw), "hunter2") {
		t.Error("part of the secret reached the serialized record")
	}
}

func TestNoRawContentWhenTheSourceEmitsNone(t *testing.T) {
	span := &tracev1.Span{
		TraceId: id(t, testTraceID), SpanId: id(t, testSpanID),
		Attributes: []*commonv1.KeyValue{
			str("mcp.method.name", "tools/call"), str("gen_ai.tool.name", "t"),
		},
	}
	ex, _ := FromSpan(span)
	if ex.ArgumentsFingerprintStatus != observation.NotEmitted {
		t.Errorf("availability = %s, want not_emitted_by_source - a refusal must not be claimed for content that was never offered",
			ex.ArgumentsFingerprintStatus)
	}
}
