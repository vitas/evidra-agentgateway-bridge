package otlphttp

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	collogsv1 "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	coltracev1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestLogsHandlerAcceptsOTLPHTTPRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/logs", bytes.NewReader(sampleOTLPPayload(t)))
	req.Header.Set("Content-Type", "application/x-protobuf")
	rr := httptest.NewRecorder()

	NewLogsHandler(nil).ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status=%d, want %d", rr.Code, http.StatusAccepted)
	}
}

func TestTracesHandlerAcceptsOTLPHTTPRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(sampleOTLPTracePayload(t)))
	req.Header.Set("Content-Type", "application/x-protobuf")
	rr := httptest.NewRecorder()

	NewTracesHandler(nil).ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status=%d, want %d", rr.Code, http.StatusAccepted)
	}
}

func sampleOTLPPayload(t *testing.T) []byte {
	t.Helper()

	req := &collogsv1.ExportLogsServiceRequest{
		ResourceLogs: []*logsv1.ResourceLogs{
			{
				Resource: &resourcev1.Resource{
					Attributes: []*commonv1.KeyValue{
						{
							Key: "service.name",
							Value: &commonv1.AnyValue{
								Value: &commonv1.AnyValue_StringValue{StringValue: "agentgateway"},
							},
						},
					},
				},
				ScopeLogs: []*logsv1.ScopeLogs{
					{
						LogRecords: []*logsv1.LogRecord{
							{
								TimeUnixNano: 1742383200000000000,
								TraceId:      bytes.Repeat([]byte{0x11}, 16),
								SpanId:       bytes.Repeat([]byte{0x22}, 8),
								Attributes: []*commonv1.KeyValue{
									{
										Key: "mcp.method.name",
										Value: &commonv1.AnyValue{
											Value: &commonv1.AnyValue_StringValue{StringValue: "tools/call"},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	payload, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return payload
}

func sampleOTLPTracePayload(t *testing.T) []byte {
	t.Helper()

	req := &coltracev1.ExportTraceServiceRequest{
		ResourceSpans: []*tracev1.ResourceSpans{
			{
				Resource: &resourcev1.Resource{
					Attributes: []*commonv1.KeyValue{
						stringAttr("service.name", "agentgateway"),
					},
				},
				ScopeSpans: []*tracev1.ScopeSpans{
					{
						Spans: []*tracev1.Span{
							{
								TraceId:           bytes.Repeat([]byte{0x11}, 16),
								SpanId:            bytes.Repeat([]byte{0x22}, 8),
								StartTimeUnixNano: 1742383200000000000,
								EndTimeUnixNano:   1742383205000000000,
								Attributes: []*commonv1.KeyValue{
									stringAttr("mcp.method", "tools/call"),
									stringAttr("mcp.session_id", "session-1"),
									stringAttr("mcp.tool.name", "kubectl_apply"),
									intAttr("http.status_code", 200),
								},
							},
						},
					},
				},
			},
		},
	}

	payload, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal trace payload: %v", err)
	}
	return payload
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
