package bridge

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/vitas/evidra-agentgateway-bridge/internal/evidra"
	"github.com/vitas/evidra-agentgateway-bridge/internal/normalize"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
)

type ingestClient interface {
	IngestPrescribe(ctx context.Context, req evidra.PrescribeRequest) (evidra.PrescribeResponse, error)
	IngestReport(ctx context.Context, req evidra.ReportRequest) (evidra.ReportResponse, error)
}

type Processor struct {
	client        ingestClient
	mu            sync.Mutex
	prescriptions map[string]string
}

func NewProcessor(client ingestClient) *Processor {
	return &Processor{
		client:        client,
		prescriptions: make(map[string]string),
	}
}

func (p *Processor) ConsumeLogRecords(ctx context.Context, records []*logsv1.LogRecord) error {
	if p == nil || p.client == nil {
		return nil
	}

	for _, record := range records {
		if err := p.consumeMapped(ctx, normalize.MapAgentGatewayRecord(record)); err != nil {
			return err
		}
	}

	return nil
}

func (p *Processor) ConsumeSpans(ctx context.Context, spans []*tracev1.Span) error {
	if p == nil || p.client == nil {
		return nil
	}

	for _, span := range spans {
		if err := p.consumeMapped(ctx, normalize.MapAgentGatewaySpan(span)); err != nil {
			return err
		}
	}

	return nil
}

func (p *Processor) consumeMapped(ctx context.Context, mapped normalize.MappedEvents) error {
	for _, action := range mapped.Actions {
		resp, err := p.client.IngestPrescribe(ctx, prescribeRequest(action))
		if err != nil {
			return fmt.Errorf("ingest prescribe: %w", err)
		}
		p.storePrescription(correlationKey(
			action.SessionKey,
			action.TraceID,
			action.MethodName,
			action.ToolName,
			action.Target,
		), resp.PrescriptionID)
	}
	for _, outcome := range mapped.Outcomes {
		prescriptionID, ok := p.lookupPrescription(correlationKey(
			outcome.SessionKey,
			outcome.TraceID,
			outcome.MethodName,
			outcome.ToolName,
			outcome.Target,
		))
		if !ok {
			continue
		}
		if _, err := p.client.IngestReport(ctx, reportRequest(outcome, prescriptionID)); err != nil {
			return fmt.Errorf("ingest report: %w", err)
		}
	}
	return nil
}

func (p *Processor) storePrescription(key, prescriptionID string) {
	if key == "" || prescriptionID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prescriptions[key] = prescriptionID
}

func (p *Processor) lookupPrescription(key string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	prescriptionID, ok := p.prescriptions[key]
	return prescriptionID, ok
}

func prescribeRequest(action normalize.ObservedActionEvent) evidra.PrescribeRequest {
	key := correlationKey(action.SessionKey, action.TraceID, action.MethodName, action.ToolName, action.Target)
	return evidra.PrescribeRequest{
		ContractVersion: evidra.ContractVersionV1,
		Claim: &evidra.Claim{
			Source: "agentgateway-bridge",
			Key:    "action:" + key,
		},
		Actor:       actorMetadata(),
		SessionID:   firstNonEmpty(action.SessionKey, action.TraceID),
		OperationID: key,
		TraceID:     action.TraceID,
		SpanID:      action.SpanID,
		Flavor:      normalize.DefaultFlavor,
		Evidence:    &evidra.EvidenceMetadata{Kind: normalize.DefaultEvidenceKind},
		Source:      &evidra.SourceMetadata{System: normalize.DefaultSourceSystem},
		ScopeDimensions: map[string]string{
			"tool":          action.ToolName,
			"target":        action.Target,
			"resource_type": action.ResourceType,
		},
		SmartTarget: &evidra.SmartTarget{
			Tool:      firstNonEmpty(action.ToolName, "unknown-tool"),
			Operation: firstNonEmpty(action.MethodName, "unknown-operation"),
			Resource:  firstNonEmpty(action.ResourceURI, action.Target, action.ToolName),
		},
	}
}

func reportRequest(outcome normalize.ObservedOutcomeEvent, prescriptionID string) evidra.ReportRequest {
	exitCode, verdict := verdictFromOutcome(outcome)
	key := correlationKey(outcome.SessionKey, outcome.TraceID, outcome.MethodName, outcome.ToolName, outcome.Target)
	return evidra.ReportRequest{
		ContractVersion: evidra.ContractVersionV1,
		Claim: &evidra.Claim{
			Source: "agentgateway-bridge",
			Key:    "outcome:" + key,
		},
		Actor:          actorMetadata(),
		SessionID:      firstNonEmpty(outcome.SessionKey, outcome.TraceID),
		OperationID:    key,
		TraceID:        outcome.TraceID,
		SpanID:         outcome.SpanID,
		Flavor:         normalize.DefaultFlavor,
		Evidence:       &evidra.EvidenceMetadata{Kind: normalize.DefaultEvidenceKind},
		Source:         &evidra.SourceMetadata{System: normalize.DefaultSourceSystem},
		PrescriptionID: prescriptionID,
		Verdict:        verdict,
		ExitCode:       &exitCode,
		ScopeDimensions: map[string]string{
			"tool":   outcome.ToolName,
			"target": outcome.Target,
			"status": outcome.Status,
		},
	}
}

func actorMetadata() evidra.Actor {
	return evidra.Actor{
		Type:       "system",
		ID:         "agentgateway",
		Provenance: "otel",
	}
}

func verdictFromOutcome(outcome normalize.ObservedOutcomeEvent) (int, string) {
	statusCode, err := strconv.Atoi(strings.TrimSpace(outcome.Status))
	if err != nil {
		if outcome.ErrorCode != "" {
			return -1, "error"
		}
		return 1, "failure"
	}
	if statusCode >= 200 && statusCode < 400 {
		return 0, "success"
	}
	return 1, "failure"
}

func correlationKey(parts ...string) string {
	trimmed := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		trimmed = append(trimmed, part)
	}
	return strings.Join(trimmed, "|")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
