package otlphttp

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/vitas/evidra-agentgateway-bridge/internal/bridge"
	"github.com/vitas/evidra-agentgateway-bridge/internal/evidra"
	collogsv1 "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	coltracev1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestLogsHandlerForwardsMappedLifecycle(t *testing.T) {
	t.Parallel()

	client := &fakeIngestClient{
		prescribeResponse: evidra.PrescribeResponse{PrescriptionID: "presc-1"},
	}
	handler := NewLogsHandler(bridge.NewProcessor(client))

	req := httptest.NewRequest(http.MethodPost, "/v1/logs", bytes.NewReader(sampleFixturePayload(t)))
	req.Header.Set("Content-Type", "application/x-protobuf")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status=%d, want %d body=%s", rr.Code, http.StatusAccepted, rr.Body.String())
	}
	if len(client.prescribeRequests) != 1 {
		t.Fatalf("prescribe requests=%d, want 1", len(client.prescribeRequests))
	}
	if len(client.reportRequests) != 1 {
		t.Fatalf("report requests=%d, want 1", len(client.reportRequests))
	}
}

func TestTracesHandlerForwardsMappedLifecycle(t *testing.T) {
	t.Parallel()

	client := &fakeIngestClient{
		prescribeResponse: evidra.PrescribeResponse{PrescriptionID: "presc-trace"},
	}
	handler := NewTracesHandler(bridge.NewProcessor(client))

	req := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(sampleTraceFixturePayload(t)))
	req.Header.Set("Content-Type", "application/x-protobuf")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status=%d, want %d body=%s", rr.Code, http.StatusAccepted, rr.Body.String())
	}
	if len(client.prescribeRequests) != 1 {
		t.Fatalf("prescribe requests=%d, want 1", len(client.prescribeRequests))
	}
	if len(client.reportRequests) != 1 {
		t.Fatalf("report requests=%d, want 1", len(client.reportRequests))
	}
}

type fakeIngestClient struct {
	prescribeResponse evidra.PrescribeResponse
	prescribeRequests []evidra.PrescribeRequest
	reportRequests    []evidra.ReportRequest
}

func (f *fakeIngestClient) IngestPrescribe(_ context.Context, req evidra.PrescribeRequest) (evidra.PrescribeResponse, error) {
	f.prescribeRequests = append(f.prescribeRequests, req)
	return f.prescribeResponse, nil
}

func (f *fakeIngestClient) IngestReport(_ context.Context, req evidra.ReportRequest) (evidra.ReportResponse, error) {
	f.reportRequests = append(f.reportRequests, req)
	return evidra.ReportResponse{EntryID: "report-1"}, nil
}

func sampleFixturePayload(t *testing.T) []byte {
	t.Helper()

	request := &collogsv1.ExportLogsServiceRequest{
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
							mustLoadFixtureRecord(t, "log_record_minimal.json"),
							mustLoadFixtureRecord(t, "log_record_outcome.json"),
						},
					},
				},
			},
		},
	}

	payload, err := proto.Marshal(request)
	if err != nil {
		t.Fatalf("marshal fixture payload: %v", err)
	}
	return payload
}

func sampleTraceFixturePayload(t *testing.T) []byte {
	t.Helper()

	request := &coltracev1.ExportTraceServiceRequest{
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
									stringAttr("mcp.target", "kind-demo"),
									intAttr("http.status_code", 200),
								},
							},
						},
					},
				},
			},
		},
	}

	payload, err := proto.Marshal(request)
	if err != nil {
		t.Fatalf("marshal trace fixture payload: %v", err)
	}
	return payload
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
