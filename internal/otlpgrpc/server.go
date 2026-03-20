package otlpgrpc

import (
	"context"
	"net"

	coltracev1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
)

// TraceConsumer processes flattened OTLP spans.
type TraceConsumer interface {
	ConsumeSpans(ctx context.Context, spans []*tracev1.Span) error
}

// Server wraps a gRPC server that accepts OTLP trace exports.
type Server struct {
	coltracev1.UnimplementedTraceServiceServer
	grpcServer *grpc.Server
	consumer   TraceConsumer
}

// NewServer creates a gRPC OTLP trace receiver.
func NewServer(consumer TraceConsumer) *Server {
	s := &Server{
		grpcServer: grpc.NewServer(),
		consumer:   consumer,
	}
	coltracev1.RegisterTraceServiceServer(s.grpcServer, s)
	return s
}

// Serve starts the gRPC server on the given listener.
func (s *Server) Serve(lis net.Listener) error {
	return s.grpcServer.Serve(lis)
}

// GracefulStop stops the gRPC server gracefully.
func (s *Server) GracefulStop() {
	s.grpcServer.GracefulStop()
}

// Export implements the OTLP TraceService Export RPC.
func (s *Server) Export(ctx context.Context, req *coltracev1.ExportTraceServiceRequest) (*coltracev1.ExportTraceServiceResponse, error) {
	if s.consumer != nil {
		spans := flattenSpans(req)
		if err := s.consumer.ConsumeSpans(ctx, spans); err != nil {
			return nil, err
		}
	}
	return &coltracev1.ExportTraceServiceResponse{}, nil
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
