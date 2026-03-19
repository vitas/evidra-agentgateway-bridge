package otlphttp

import (
	"context"
	"io"
	"net/http"

	collogsv1 "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	coltracev1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

type RecordConsumer interface {
	ConsumeLogRecords(ctx context.Context, records []*logsv1.LogRecord) error
}

type TraceConsumer interface {
	ConsumeSpans(ctx context.Context, spans []*tracev1.Span) error
}

func NewLogsHandler(consumer RecordConsumer) http.Handler {
	return logsHandler{consumer: consumer}
}

func NewTracesHandler(consumer TraceConsumer) http.Handler {
	return tracesHandler{consumer: consumer}
}

type logsHandler struct {
	consumer RecordConsumer
}

type tracesHandler struct {
	consumer TraceConsumer
}

func (h logsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	var req collogsv1.ExportLogsServiceRequest
	if err := proto.Unmarshal(body, &req); err != nil {
		http.Error(w, "decode otlp logs", http.StatusBadRequest)
		return
	}

	if h.consumer != nil {
		if err := h.consumer.ConsumeLogRecords(r.Context(), flattenLogRecords(&req)); err != nil {
			http.Error(w, "consume logs", http.StatusInternalServerError)
			return
		}
	}

	w.WriteHeader(http.StatusAccepted)
}

func (h tracesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	var req coltracev1.ExportTraceServiceRequest
	if err := proto.Unmarshal(body, &req); err != nil {
		http.Error(w, "decode otlp traces", http.StatusBadRequest)
		return
	}

	if h.consumer != nil {
		if err := h.consumer.ConsumeSpans(r.Context(), flattenSpans(&req)); err != nil {
			http.Error(w, "consume traces", http.StatusInternalServerError)
			return
		}
	}

	w.WriteHeader(http.StatusAccepted)
}

func flattenLogRecords(req *collogsv1.ExportLogsServiceRequest) []*logsv1.LogRecord {
	var records []*logsv1.LogRecord
	for _, resourceLogs := range req.ResourceLogs {
		for _, scopeLogs := range resourceLogs.ScopeLogs {
			records = append(records, scopeLogs.LogRecords...)
		}
	}
	return records
}

func flattenSpans(req *coltracev1.ExportTraceServiceRequest) []*tracev1.Span {
	var spans []*tracev1.Span
	for _, resourceSpans := range req.ResourceSpans {
		for _, scopeSpans := range resourceSpans.ScopeSpans {
			spans = append(spans, scopeSpans.Spans...)
		}
	}
	return spans
}
