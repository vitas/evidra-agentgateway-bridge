package normalize

import (
	"os"
	"path/filepath"
	"testing"

	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestMapAgentGatewayActionFixture(t *testing.T) {
	record := mustLoadFixtureRecord(t, "log_record_minimal.json")

	mapped := MapAgentGatewayRecord(record)
	if len(mapped.Actions) != 1 {
		t.Fatalf("actions=%d, want 1", len(mapped.Actions))
	}
	if len(mapped.Outcomes) != 0 {
		t.Fatalf("outcomes=%d, want 0", len(mapped.Outcomes))
	}
}

func TestMapAgentGatewayOutcomeFixture(t *testing.T) {
	record := mustLoadFixtureRecord(t, "log_record_outcome.json")

	mapped := MapAgentGatewayRecord(record)
	if len(mapped.Outcomes) != 1 {
		t.Fatalf("outcomes=%d, want 1", len(mapped.Outcomes))
	}
}

func TestMapAgentGatewayTraceSpan(t *testing.T) {
	span := &tracev1.Span{
		TraceId:           bytes16(0x11),
		SpanId:            bytes8(0x22),
		StartTimeUnixNano: 1742383200000000000,
		EndTimeUnixNano:   1742383205000000000,
		Attributes: []*commonv1.KeyValue{
			stringAttr("mcp.method", "tools/call"),
			stringAttr("mcp.session_id", "session-1"),
			stringAttr("mcp.tool.name", "kubectl_apply"),
			stringAttr("mcp.target", "kind-demo"),
			intAttr("http.status_code", 200),
		},
	}

	mapped := MapAgentGatewaySpan(span)
	if len(mapped.Actions) != 1 {
		t.Fatalf("actions=%d, want 1", len(mapped.Actions))
	}
	if len(mapped.Outcomes) != 1 {
		t.Fatalf("outcomes=%d, want 1", len(mapped.Outcomes))
	}
	if mapped.Actions[0].MethodName != "tools/call" {
		t.Fatalf("method=%q, want tools/call", mapped.Actions[0].MethodName)
	}
	if mapped.Outcomes[0].Status != "200" {
		t.Fatalf("status=%q, want 200", mapped.Outcomes[0].Status)
	}
}

func TestMapAgentGatewayTraceSpan_IgnoresInitialize(t *testing.T) {
	span := &tracev1.Span{
		TraceId:           bytes16(0x33),
		SpanId:            bytes8(0x44),
		StartTimeUnixNano: 1742383200000000000,
		EndTimeUnixNano:   1742383205000000000,
		Attributes: []*commonv1.KeyValue{
			stringAttr("mcp.method", "initialize"),
			stringAttr("mcp.session_id", "session-1"),
			intAttr("http.status_code", 200),
		},
	}

	mapped := MapAgentGatewaySpan(span)
	if len(mapped.Actions) != 0 {
		t.Fatalf("actions=%d, want 0", len(mapped.Actions))
	}
	if len(mapped.Outcomes) != 0 {
		t.Fatalf("outcomes=%d, want 0", len(mapped.Outcomes))
	}
}

func mustLoadFixtureRecord(t *testing.T, name string) *logsv1.LogRecord {
	t.Helper()

	path := filepath.Join("..", "..", "testdata", "agentgateway", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}

	var record logsv1.LogRecord
	if err := protojson.Unmarshal(data, &record); err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}

	return &record
}

func stringAttr(key, value string) *commonv1.KeyValue {
	return &commonv1.KeyValue{
		Key: key,
		Value: &commonv1.AnyValue{
			Value: &commonv1.AnyValue_StringValue{StringValue: value},
		},
	}
}

func intAttr(key string, value int64) *commonv1.KeyValue {
	return &commonv1.KeyValue{
		Key: key,
		Value: &commonv1.AnyValue{
			Value: &commonv1.AnyValue_IntValue{IntValue: value},
		},
	}
}

func bytes16(value byte) []byte {
	return []byte{value, value, value, value, value, value, value, value, value, value, value, value, value, value, value, value}
}

func bytes8(value byte) []byte {
	return []byte{value, value, value, value, value, value, value, value}
}
