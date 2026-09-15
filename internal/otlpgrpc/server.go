// Package otlpgrpc receives OTLP over gRPC, which is what AgentGateway's tracing and
// access-log export use by default.
//
// Both signals are served, and that is not optional: the execution facts arrive on spans while
// a CEL-projected operation id arrives in an access-log record, so a traces-only gRPC receiver
// would accept telemetry and be structurally unable to correlate any of it.
package otlpgrpc

import (
	"context"
	"net"

	collogsv1 "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	coltracev1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
)

// TraceConsumer processes flattened OTLP spans.
type TraceConsumer interface {
	ConsumeSpans(ctx context.Context, spans []*tracev1.Span) error
}

// LogConsumer processes flattened OTLP log records.
type LogConsumer interface {
	ConsumeLogRecords(ctx context.Context, records []*logsv1.LogRecord) error
}

// Server wraps a gRPC server that accepts OTLP trace and log exports.
//
// The two services are separate types on one grpc.Server, because both name their RPC
// `Export` with incompatible signatures and Go has no way to satisfy both on one receiver.
type Server struct {
	grpcServer *grpc.Server
}

// NewServer creates a gRPC OTLP receiver. Either consumer may be nil, in which case that
// signal is accepted and discarded - which is what a traces-only deployment wants, and is
// reported through the processor's stats rather than by refusing the export.
func NewServer(traces TraceConsumer, logs LogConsumer) *Server {
	grpcServer := grpc.NewServer()
	coltracev1.RegisterTraceServiceServer(grpcServer, &traceService{consumer: traces})
	collogsv1.RegisterLogsServiceServer(grpcServer, &logsService{consumer: logs})
	return &Server{grpcServer: grpcServer}
}

type traceService struct {
	coltracev1.UnimplementedTraceServiceServer
	consumer TraceConsumer
}

// Export implements the OTLP TraceService Export RPC.
func (s *traceService) Export(ctx context.Context, req *coltracev1.ExportTraceServiceRequest) (*coltracev1.ExportTraceServiceResponse, error) {
	if s.consumer != nil {
		if err := s.consumer.ConsumeSpans(ctx, flattenSpans(req)); err != nil {
			return nil, err
		}
	}
	return &coltracev1.ExportTraceServiceResponse{}, nil
}

type logsService struct {
	collogsv1.UnimplementedLogsServiceServer
	consumer LogConsumer
}

// Export implements the OTLP LogsService Export RPC.
func (s *logsService) Export(ctx context.Context, req *collogsv1.ExportLogsServiceRequest) (*collogsv1.ExportLogsServiceResponse, error) {
	if s.consumer != nil {
		if err := s.consumer.ConsumeLogRecords(ctx, flattenLogRecords(req)); err != nil {
			return nil, err
		}
	}
	return &collogsv1.ExportLogsServiceResponse{}, nil
}

// Serve starts the gRPC server on the given listener.
func (s *Server) Serve(lis net.Listener) error {
	return s.grpcServer.Serve(lis)
}

// GracefulStop stops the gRPC server gracefully.
func (s *Server) GracefulStop() {
	s.grpcServer.GracefulStop()
}

// Stop immediately terminates active RPCs. Shutdown uses it only when the shared graceful
// shutdown deadline has expired.
func (s *Server) Stop() {
	s.grpcServer.Stop()
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

func flattenLogRecords(req *collogsv1.ExportLogsServiceRequest) []*logsv1.LogRecord {
	var records []*logsv1.LogRecord
	for _, resourceLogs := range req.ResourceLogs {
		for _, scopeLogs := range resourceLogs.ScopeLogs {
			records = append(records, scopeLogs.LogRecords...)
		}
	}
	return records
}
