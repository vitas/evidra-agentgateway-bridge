package replay

import "testing"

func TestBuildFixturePayload_IncludesRequestedRecords(t *testing.T) {
	payload, err := BuildFixturePayload([]string{
		"testdata/agentgateway/log_record_minimal.json",
		"testdata/agentgateway/log_record_outcome.json",
	})
	if err != nil {
		t.Fatalf("BuildFixturePayload: %v", err)
	}
	if len(payload) == 0 {
		t.Fatal("expected non-empty OTLP payload")
	}
}
