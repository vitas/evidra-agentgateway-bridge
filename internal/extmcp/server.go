// Package extmcp implements the intentionally small ExtMcp capture hook used
// by the v1.5.0 spike. It is an observer: requests and responses are passed
// through, while only bounded metadata from metadata_context is retained.
package extmcp

import (
	"context"
	"fmt"
	"sort"
	"sync"

	api "github.com/vitas/evidra-agentgateway-bridge/internal/extmcp/api"
	"google.golang.org/protobuf/types/known/structpb"
)

const operationContextKey = "evidra_operation_id"

// Capture is the bounded lifecycle fact retained by the spike. Raw MCP
// request/response bytes are deliberately represented only by their lengths,
// which remain zero because this observer never stores payloads.
type Capture struct {
	ServiceNames     []string
	Method           string
	RequestSeen      bool
	ResponseSeen     bool
	OperationContext string
	RawPersisted     bool
}

// Store is an in-memory test/spike store. It is not a persistence layer.
type Store struct {
	mu       sync.Mutex
	captures map[string]*Capture
}

func NewStore() *Store { return &Store{captures: make(map[string]*Capture)} }

func (s *Store) mark(operation string, services []string, method string, start, finish bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	capture := s.captures[operation]
	if capture == nil {
		capture = &Capture{ServiceNames: append([]string(nil), services...), OperationContext: operation, Method: method}
		s.captures[operation] = capture
	}
	capture.RequestSeen = capture.RequestSeen || start
	capture.ResponseSeen = capture.ResponseSeen || finish
}

// Snapshot returns copies sorted by the stable operation context.
func (s *Store) Snapshot() []Capture {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Capture, 0, len(s.captures))
	for _, capture := range s.captures {
		result = append(result, *capture)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].OperationContext < result[j].OperationContext
	})
	return result
}

// Server implements the generated AgentGateway ExtMcp v1.5.0 API.
type Server struct {
	api.UnimplementedExtMcpServer
	store *Store
}

func NewServer(store *Store) *Server {
	if store == nil {
		store = NewStore()
	}
	return &Server{store: store}
}

func (s *Server) Store() *Store { return s.store }

func (s *Server) CheckRequest(_ context.Context, req *api.McpRequest) (*api.McpRequestResult, error) {
	operation, err := operationContext(req.GetMetadataContext())
	if err != nil {
		return nil, err
	}
	s.store.mark(operation, req.GetServiceNames(), req.GetMethod(), true, false)
	// Do not inspect or copy req.McpRequest. This is the privacy boundary.
	return &api.McpRequestResult{Result: &api.McpRequestResult_Pass{Pass: &api.Pass{}}}, nil
}

func (s *Server) CheckResponse(_ context.Context, resp *api.McpResponse) (*api.McpResponseResult, error) {
	operation, err := operationContext(resp.GetMetadataContext())
	if err != nil {
		return nil, err
	}
	s.store.mark(operation, resp.GetServiceNames(), resp.GetMethod(), false, true)
	// Do not inspect or copy resp.McpResponse. This is the privacy boundary.
	return &api.McpResponseResult{Result: &api.McpResponseResult_Pass{Pass: &api.Pass{}}}, nil
}

func operationContext(metadata *structpb.Struct) (string, error) {
	if metadata == nil {
		return "", fmt.Errorf("missing authoritative operation context")
	}
	value, ok := metadata.GetFields()[operationContextKey]
	if !ok || value.GetStringValue() == "" {
		return "", fmt.Errorf("missing authoritative operation context %q", operationContextKey)
	}
	return value.GetStringValue(), nil
}
