package otlpgrpc

import (
	"context"
	"net"
	"testing"

	coltracev1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type spyConsumer struct {
	spans []*tracev1.Span
}

func (s *spyConsumer) ConsumeSpans(_ context.Context, spans []*tracev1.Span) error {
	s.spans = append(s.spans, spans...)
	return nil
}

func TestServer_Export(t *testing.T) {
	t.Parallel()

	spy := &spyConsumer{}
	srv := NewServer(spy)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	client := coltracev1.NewTraceServiceClient(conn)
	_, err = client.Export(context.Background(), &coltracev1.ExportTraceServiceRequest{
		ResourceSpans: []*tracev1.ResourceSpans{{
			ScopeSpans: []*tracev1.ScopeSpans{{
				Spans: []*tracev1.Span{{
					Name: "tools/call",
					Attributes: []*commonv1.KeyValue{
						{Key: "mcp.method.name", Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: "tools/call"}}},
					},
				}},
			}},
		}},
	})
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	if len(spy.spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spy.spans))
	}
	if spy.spans[0].Name != "tools/call" {
		t.Errorf("expected span name tools/call, got %s", spy.spans[0].Name)
	}
}

func TestServer_NilConsumer(t *testing.T) {
	t.Parallel()

	srv := NewServer(nil)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	client := coltracev1.NewTraceServiceClient(conn)
	_, err = client.Export(context.Background(), &coltracev1.ExportTraceServiceRequest{})
	if err != nil {
		t.Fatalf("Export with nil consumer should succeed: %v", err)
	}
}
