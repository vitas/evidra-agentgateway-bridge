package evidra

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientIngestPrescribePostsJSON(t *testing.T) {
	t.Parallel()

	var (
		gotAuth string
		gotPath string
		gotBody PrescribeRequest
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(PrescribeResponse{
			EntryID:        "entry-1",
			PrescriptionID: "presc-1",
			EffectiveRisk:  "medium",
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key", server.Client())
	resp, err := client.IngestPrescribe(context.Background(), PrescribeRequest{
		ContractVersion: "v1",
		Actor: Actor{
			Type:       "system",
			ID:         "agentgateway",
			Provenance: "otel",
		},
		SessionID:   "session-1",
		OperationID: "operation-1",
		TraceID:     "trace-1",
		Flavor:      "imperative",
		Evidence:    &EvidenceMetadata{Kind: "observed"},
		Source:      &SourceMetadata{System: "agentgateway"},
		SmartTarget: &SmartTarget{
			Tool:      "kubectl_apply",
			Operation: "tools/call",
			Resource:  "kind-demo",
		},
	})
	if err != nil {
		t.Fatalf("IngestPrescribe: %v", err)
	}
	if gotPath != "/v1/evidence/ingest/prescribe" {
		t.Fatalf("path=%q, want %q", gotPath, "/v1/evidence/ingest/prescribe")
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("auth=%q, want %q", gotAuth, "Bearer test-key")
	}
	if gotBody.Source == nil || gotBody.Source.System != "agentgateway" {
		t.Fatalf("source=%#v, want agentgateway", gotBody.Source)
	}
	if resp.PrescriptionID != "presc-1" {
		t.Fatalf("prescription_id=%q, want %q", resp.PrescriptionID, "presc-1")
	}
}

func TestClientIngestReportPostsJSON(t *testing.T) {
	t.Parallel()

	var gotPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ReportResponse{
			EntryID: "entry-2",
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key", server.Client())
	exitCode := 0
	_, err := client.IngestReport(context.Background(), ReportRequest{
		ContractVersion: "v1",
		Actor: Actor{
			Type:       "system",
			ID:         "agentgateway",
			Provenance: "otel",
		},
		SessionID:      "session-1",
		OperationID:    "operation-1",
		TraceID:        "trace-1",
		Flavor:         "imperative",
		Evidence:       &EvidenceMetadata{Kind: "observed"},
		Source:         &SourceMetadata{System: "agentgateway"},
		PrescriptionID: "presc-1",
		Verdict:        "success",
		ExitCode:       &exitCode,
	})
	if err != nil {
		t.Fatalf("IngestReport: %v", err)
	}
	if gotPath != "/v1/evidence/ingest/report" {
		t.Fatalf("path=%q, want %q", gotPath, "/v1/evidence/ingest/report")
	}
}
