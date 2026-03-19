package evidra

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const ContractVersionV1 = "v1"

type Actor struct {
	Type       string `json:"type"`
	ID         string `json:"id"`
	Provenance string `json:"provenance"`
}

type Claim struct {
	Source string `json:"source,omitempty"`
	Key    string `json:"key,omitempty"`
}

type EvidenceMetadata struct {
	Kind string `json:"kind,omitempty"`
}

type SourceMetadata struct {
	System string `json:"system,omitempty"`
}

type SmartTarget struct {
	Tool      string `json:"tool"`
	Operation string `json:"operation"`
	Resource  string `json:"resource"`
	Namespace string `json:"namespace,omitempty"`
}

type PrescribeRequest struct {
	ContractVersion string            `json:"contract_version"`
	Claim           *Claim            `json:"claim,omitempty"`
	Actor           Actor             `json:"actor"`
	SessionID       string            `json:"session_id"`
	OperationID     string            `json:"operation_id"`
	TraceID         string            `json:"trace_id"`
	SpanID          string            `json:"span_id,omitempty"`
	ParentSpanID    string            `json:"parent_span_id,omitempty"`
	ScopeDimensions map[string]string `json:"scope_dimensions,omitempty"`
	Flavor          string            `json:"flavor"`
	Evidence        *EvidenceMetadata `json:"evidence,omitempty"`
	Source          *SourceMetadata   `json:"source,omitempty"`
	SmartTarget     *SmartTarget      `json:"smart_target,omitempty"`
}

type ReportRequest struct {
	ContractVersion string            `json:"contract_version"`
	Claim           *Claim            `json:"claim,omitempty"`
	Actor           Actor             `json:"actor"`
	SessionID       string            `json:"session_id"`
	OperationID     string            `json:"operation_id"`
	TraceID         string            `json:"trace_id"`
	SpanID          string            `json:"span_id,omitempty"`
	ParentSpanID    string            `json:"parent_span_id,omitempty"`
	ScopeDimensions map[string]string `json:"scope_dimensions,omitempty"`
	Flavor          string            `json:"flavor"`
	Evidence        *EvidenceMetadata `json:"evidence,omitempty"`
	Source          *SourceMetadata   `json:"source,omitempty"`
	PrescriptionID  string            `json:"prescription_id"`
	Verdict         string            `json:"verdict"`
	ExitCode        *int              `json:"exit_code,omitempty"`
}

type PrescribeResponse struct {
	EntryID        string `json:"entry_id"`
	PrescriptionID string `json:"prescription_id"`
	EffectiveRisk  string `json:"effective_risk,omitempty"`
	Duplicate      bool   `json:"duplicate,omitempty"`
}

type ReportResponse struct {
	EntryID       string `json:"entry_id"`
	EffectiveRisk string `json:"effective_risk,omitempty"`
	Duplicate     bool   `json:"duplicate,omitempty"`
}

type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func NewClient(baseURL, apiKey string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  strings.TrimSpace(apiKey),
		http:    httpClient,
	}
}

func (c *Client) IngestPrescribe(ctx context.Context, req PrescribeRequest) (PrescribeResponse, error) {
	var resp PrescribeResponse
	err := c.doJSON(ctx, "/v1/evidence/ingest/prescribe", req, &resp)
	return resp, err
}

func (c *Client) IngestReport(ctx context.Context, req ReportRequest) (ReportResponse, error) {
	var resp ReportResponse
	err := c.doJSON(ctx, "/v1/evidence/ingest/report", req, &resp)
	return resp, err
}

func (c *Client) doJSON(ctx context.Context, path string, requestBody any, responseBody any) error {
	body, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("unexpected status: %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(responseBody); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
