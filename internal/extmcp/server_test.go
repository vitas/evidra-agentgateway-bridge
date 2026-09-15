package extmcp

import (
	"context"
	"reflect"
	"strings"
	"testing"

	api "github.com/vitas/evidra-agentgateway-bridge/internal/extmcp/api"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestSnapshotIsSortedByOperationContext(t *testing.T) {
	store := NewStore()
	for _, operation := range []string{"z-last", "a-first", "m-middle"} {
		store.mark(operation, []string{"everything"}, "tools/call", true, false)
	}

	got := make([]string, 0, 3)
	for _, capture := range store.Snapshot() {
		got = append(got, capture.OperationContext)
	}
	if want := []string{"a-first", "m-middle", "z-last"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot order = %v, want %v", got, want)
	}
}

func TestServerCaptureMatrix(t *testing.T) {
	tests := []struct {
		name       string
		operation  string
		request    *api.McpRequest
		response   *api.McpResponse
		wantStart  bool
		wantFinish bool
	}{
		{name: "success echo", operation: "success", request: request("success"), response: response("success"), wantStart: true, wantFinish: true},
		{name: "tool error", operation: "tool_error", request: request("tool_error"), response: response("tool_error"), wantStart: true, wantFinish: true},
		{name: "cancelled slow then cancel", operation: "cancelled", request: request("cancelled"), response: response("cancelled"), wantStart: true, wantFinish: true},
		{name: "missing response disconnect", operation: "missing", request: request("missing"), wantStart: true, wantFinish: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := NewStore()
			server := NewServer(store)
			if _, err := server.CheckRequest(context.Background(), tc.request); err != nil {
				t.Fatal(err)
			}
			if tc.response != nil {
				if _, err := server.CheckResponse(context.Background(), tc.response); err != nil {
					t.Fatal(err)
				}
			}

			captures := store.Snapshot()
			if len(captures) != 1 {
				t.Fatalf("captures = %d, want 1", len(captures))
			}
			capture := captures[0]
			if capture.RequestSeen != tc.wantStart || capture.ResponseSeen != tc.wantFinish {
				t.Fatalf("capture lifecycle = (%t, %t), want (%t, %t)", capture.RequestSeen, capture.ResponseSeen, tc.wantStart, tc.wantFinish)
			}
			if capture.OperationContext != tc.operation {
				t.Fatalf("operation context = %q, want %q", capture.OperationContext, tc.operation)
			}
			if capture.RawPersisted {
				t.Fatal("raw payload persisted")
			}
		})
	}
}

func TestServerPassesOperationalRequestsAndResponsesUnchanged(t *testing.T) {
	server := NewServer(NewStore())
	req := request("pass-through")
	req.McpRequest = []byte(`{"secret":"do-not-persist"}`)
	gotRequest, err := server.CheckRequest(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if gotRequest.GetPass() == nil || gotRequest.GetMutated() != nil || gotRequest.GetError() != nil {
		t.Fatalf("request hook did not return pass-through result: %v", gotRequest)
	}
	resp := response("pass-through")
	resp.McpResponse = []byte(`{"secret":"do-not-persist"}`)
	gotResponse, err := server.CheckResponse(context.Background(), resp)
	if err != nil {
		t.Fatal(err)
	}
	if gotResponse.GetPass() == nil || gotResponse.GetMutated() != nil || gotResponse.GetError() != nil {
		t.Fatalf("response hook did not return pass-through result: %v", gotResponse)
	}
	for _, capture := range server.Store().Snapshot() {
		if capture.RawPersisted {
			t.Fatalf("raw payload leaked into capture: %+v", capture)
		}
	}
}

func TestServerRejectsMissingAuthoritativeOperationContext(t *testing.T) {
	server := NewServer(NewStore())
	req := &api.McpRequest{Method: "tools/call"}
	if _, err := server.CheckRequest(context.Background(), req); err == nil || !strings.Contains(err.Error(), "operation context") {
		t.Fatalf("missing context error = %v", err)
	}
}

func request(operation string) *api.McpRequest {
	metadata, _ := structpb.NewStruct(map[string]any{"evidra_operation_id": operation})
	return &api.McpRequest{ServiceNames: []string{"everything"}, Method: "tools/call", MetadataContext: metadata}
}

func response(operation string) *api.McpResponse {
	metadata, _ := structpb.NewStruct(map[string]any{"evidra_operation_id": operation})
	return &api.McpResponse{ServiceNames: []string{"everything"}, Method: "tools/call", MetadataContext: metadata, McpResponse: []byte(`{"ok":true}`)}
}
