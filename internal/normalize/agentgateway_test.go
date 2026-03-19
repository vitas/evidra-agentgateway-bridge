package normalize

import (
	"os"
	"path/filepath"
	"testing"

	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
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
