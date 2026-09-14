package otlphttp_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/vitas/evidra-agentgateway-bridge/internal/bridge"
	"github.com/vitas/evidra-agentgateway-bridge/internal/observation"
	"github.com/vitas/evidra-agentgateway-bridge/internal/otlphttp"
	"github.com/vitas/evidra-agentgateway-bridge/internal/sink"
	collogsv1 "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	coltracev1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

const (
	itTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	itSpanID  = "00f067aa0ba902b7"
	itOpID    = "EV-01M2FAS00J36FTV4EG5EJC1D7S"
)

func id(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad test id %q: %v", s, err)
	}
	return b
}

func kv(k, v string) *commonv1.KeyValue {
	return &commonv1.KeyValue{Key: k, Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: v}}}
}

// TestHTTPHandlersFeedTheSinkEndToEnd replaces the integration test that used to assert the
// old forwarding path. It is worth keeping as an end-to-end check rather than testing the
// handler against a spy: the previous version of this bridge returned HTTP 202 to AgentGateway
// while silently dropping every outcome, and a spy-based test passes exactly as happily as the
// real thing did. What has to be observable from outside is a record on disk.
func TestHTTPHandlersFeedTheSinkEndToEnd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observations.jsonl")
	out, err := sink.NewJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = out.Close() })

	p := bridge.NewProcessor(out, 0)
	p.SetObserver("integration-test", "test")
	mux := http.NewServeMux()
	mux.Handle("/v1/logs", otlphttp.NewLogsHandler(p))
	mux.Handle("/v1/traces", otlphttp.NewTracesHandler(p))

	// One tools/call: the operation id arrives on the access-log record, the execution facts
	// on the span. Neither alone is a usable record, which is the whole reason v1 accepts both
	// signals.
	logsReq := &collogsv1.ExportLogsServiceRequest{ResourceLogs: []*logsv1.ResourceLogs{{
		ScopeLogs: []*logsv1.ScopeLogs{{LogRecords: []*logsv1.LogRecord{{
			TraceId:      id(t, itTraceID),
			SpanId:       id(t, itSpanID),
			TimeUnixNano: uint64(time.Now().UnixNano()),
			Attributes: []*commonv1.KeyValue{
				kv("mcp.method.name", "tools/call"),
				kv("mcp.target", "alpha"),
				kv("mcp.resource.type", "tool"),
				kv("gen_ai.tool.name", "restart"),
				kv("http.status", "200"),
				kv("duration", "6"),
				kv("evidra_op", itOpID),
			},
		}}}},
	}}}
	logsBody, err := proto.Marshal(logsReq)
	if err != nil {
		t.Fatal(err)
	}
	resp := httptest.NewRecorder()
	mux.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "/v1/logs", bytes.NewReader(logsBody)))
	if resp.Code != http.StatusAccepted {
		t.Fatalf("logs export returned %d", resp.Code)
	}

	tracesReq := &coltracev1.ExportTraceServiceRequest{ResourceSpans: []*tracev1.ResourceSpans{{
		ScopeSpans: []*tracev1.ScopeSpans{{Spans: []*tracev1.Span{{
			TraceId:           id(t, itTraceID),
			SpanId:            id(t, itSpanID),
			Name:              "tools/call restart",
			StartTimeUnixNano: uint64(time.Now().Add(-6 * time.Millisecond).UnixNano()),
			EndTimeUnixNano:   uint64(time.Now().UnixNano()),
			Status:            &tracev1.Status{Code: tracev1.Status_STATUS_CODE_UNSET},
			Attributes: []*commonv1.KeyValue{
				kv("gen_ai.operation.name", "execute_tool"),
				kv("mcp.method.name", "tools/call"),
				kv("mcp.target", "alpha"),
				kv("gen_ai.tool.name", "restart"),
			},
		}}}},
	}}}
	tracesBody, err := proto.Marshal(tracesReq)
	if err != nil {
		t.Fatal(err)
	}
	resp = httptest.NewRecorder()
	mux.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(tracesBody)))
	if resp.Code != http.StatusAccepted {
		t.Fatalf("traces export returned %d", resp.Code)
	}

	written, err := sink.ReadAll(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 1 {
		t.Fatalf("sink holds %d executions, want 1 (stats %+v)", len(written), p.Stats())
	}
	ex := written[0]
	if ex.Correlation != observation.Correlated || ex.OperationID != itOpID {
		t.Errorf("correlation = %s/%q, want correlated/%s", ex.Correlation, ex.OperationID, itOpID)
	}
	if ex.Tool != "restart" || ex.Target != "alpha" {
		t.Errorf("tool/target = %q/%q", ex.Tool, ex.Target)
	}
	if ex.Status != observation.StatusSuccess {
		t.Errorf("status = %s, want success", ex.Status)
	}
	if ex.Source.ObserverID != "integration-test" {
		t.Errorf("observer id = %q", ex.Source.ObserverID)
	}
	if len(ex.SeenFrom) != 2 {
		t.Errorf("seen_from = %v, want both signals", ex.SeenFrom)
	}
	if stats := p.Stats(); stats.EmittedMerged != 1 || stats.StillPending != 0 {
		t.Errorf("stats = %+v", stats)
	}
}

// TestBadProtobufIsRejectedNotAccepted covers the failure mode that matters for a receiver: a
// malformed export must not be acknowledged, or the gateway marks it delivered and the record
// is gone from both sides.
func TestBadProtobufIsRejectedNotAccepted(t *testing.T) {
	dir := t.TempDir()
	out, err := sink.NewJSONL(filepath.Join(dir, "o.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = out.Close() })
	p := bridge.NewProcessor(out, 0)

	for _, tc := range []struct {
		path string
		body []byte
	}{
		{"/v1/logs", []byte("this is not protobuf")},
		{"/v1/traces", []byte("this is not protobuf")},
	} {
		var handler http.Handler = otlphttp.NewLogsHandler(p)
		if tc.path == "/v1/traces" {
			handler = otlphttp.NewTracesHandler(p)
		}
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, tc.path, bytes.NewReader(tc.body)))
		if resp.Code != http.StatusBadRequest {
			t.Errorf("%s with a malformed body returned %d, want 400", tc.path, resp.Code)
		}
	}
}

// TestNonToolCallTrafficIsAcceptedAndIgnored pins that unrelated spans do not become
// executions. A receiver that turned every HTTP span in a trace into a tool call would inflate
// coverage and nobody could tell from the artifact.
func TestNonToolCallTrafficIsAcceptedAndIgnored(t *testing.T) {
	dir := t.TempDir()
	out, err := sink.NewJSONL(filepath.Join(dir, "o.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = out.Close() })
	p := bridge.NewProcessor(out, 0)

	req := &coltracev1.ExportTraceServiceRequest{ResourceSpans: []*tracev1.ResourceSpans{{
		ScopeSpans: []*tracev1.ScopeSpans{{Spans: []*tracev1.Span{
			{TraceId: id(t, itTraceID), SpanId: id(t, itSpanID), Name: "POST",
				Attributes: []*commonv1.KeyValue{kv("http.request.method", "POST")}},
			{TraceId: id(t, itTraceID), SpanId: id(t, itSpanID), Name: "chat gpt",
				Attributes: []*commonv1.KeyValue{kv("gen_ai.operation.name", "chat")}},
		}}},
	}}}
	body, err := proto.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	resp := httptest.NewRecorder()
	otlphttp.NewTracesHandler(p).ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(body)))
	if resp.Code != http.StatusAccepted {
		t.Fatalf("returned %d", resp.Code)
	}
	if err := p.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	written, err := sink.ReadAll(filepath.Join(dir, "o.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 0 {
		t.Errorf("non-MCP spans became %d executions", len(written))
	}
}
