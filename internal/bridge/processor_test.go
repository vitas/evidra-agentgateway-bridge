package bridge

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vitas/evidra-agentgateway-bridge/internal/evidra"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestProcessorCorrelatesActionAndOutcome(t *testing.T) {
	t.Parallel()

	client := &fakeIngestClient{
		prescribeResponse: evidra.PrescribeResponse{PrescriptionID: "presc-1"},
	}
	processor := NewProcessor(client)

	records := []*logsv1.LogRecord{
		mustLoadFixtureRecord(t, "log_record_minimal.json"),
		mustLoadFixtureRecord(t, "log_record_outcome.json"),
	}
	if err := processor.ConsumeLogRecords(context.Background(), records); err != nil {
		t.Fatalf("ConsumeLogRecords: %v", err)
	}

	if len(client.prescribeRequests) != 1 {
		t.Fatalf("prescribe requests=%d, want 1", len(client.prescribeRequests))
	}
	if len(client.reportRequests) != 1 {
		t.Fatalf("report requests=%d, want 1", len(client.reportRequests))
	}
	if client.reportRequests[0].PrescriptionID != "presc-1" {
		t.Fatalf("report prescription_id=%q, want %q", client.reportRequests[0].PrescriptionID, "presc-1")
	}
	if client.reportRequests[0].Verdict != "success" {
		t.Fatalf("report verdict=%q, want %q", client.reportRequests[0].Verdict, "success")
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
