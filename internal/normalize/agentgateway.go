package normalize

import (
	"encoding/hex"
	"strconv"
	"time"

	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
)

type MappedEvents struct {
	Actions  []ObservedActionEvent
	Outcomes []ObservedOutcomeEvent
}

func MapAgentGatewayRecord(record *logsv1.LogRecord) MappedEvents {
	if record == nil {
		return MappedEvents{}
	}

	attrs := attributeMap(record.Attributes)
	if isOutcomeRecord(attrs) {
		outcome := NewObservedOutcomeEvent()
		outcome.Timestamp = timestampFromUnixNano(record.TimeUnixNano)
		outcome.TraceID = hex.EncodeToString(record.TraceId)
		outcome.SpanID = hex.EncodeToString(record.SpanId)
		outcome.SessionKey = firstNonEmpty(attrs["mcp.session.id"], attrs["mcp.session_id"])
		outcome.MethodName = firstNonEmpty(attrs["mcp.method.name"], attrs["mcp.method"])
		outcome.ToolName = firstNonEmpty(attrs["gen_ai.tool.name"], attrs["mcp.tool.name"], attrs["mcp.resource.name"])
		outcome.Target = attrs["mcp.target"]
		outcome.Status = firstNonEmpty(attrs["http.status"], attrs["http.status_code"], attrs["otel.status_code"])
		outcome.ErrorCode = attrs["mcp.error.code"]
		outcome.ErrorMessage = firstNonEmpty(attrs["mcp.error.message"], attrs["reason"])
		outcome.GenAI = extractGenAIUsage(attrs)
		if !hasActionIdentity(ObservedActionEvent{
			MethodName: outcome.MethodName,
			ToolName:   outcome.ToolName,
			Target:     outcome.Target,
		}) {
			return MappedEvents{}
		}
		return MappedEvents{Outcomes: []ObservedOutcomeEvent{outcome}}
	}

	action := NewObservedActionEvent()
	action.Timestamp = timestampFromUnixNano(record.TimeUnixNano)
	action.TraceID = hex.EncodeToString(record.TraceId)
	action.SpanID = hex.EncodeToString(record.SpanId)
	action.SessionKey = firstNonEmpty(attrs["mcp.session.id"], attrs["mcp.session_id"])
	action.MethodName = firstNonEmpty(attrs["mcp.method.name"], attrs["mcp.method"])
	action.ToolName = firstNonEmpty(attrs["gen_ai.tool.name"], attrs["mcp.tool.name"], attrs["mcp.resource.name"])
	action.Target = attrs["mcp.target"]
	action.ResourceType = attrs["mcp.resource.type"]
	action.ResourceURI = attrs["mcp.resource.uri"]
	action.GenAI = extractGenAIUsage(attrs)
	if !hasActionIdentity(action) {
		return MappedEvents{}
	}
	return MappedEvents{Actions: []ObservedActionEvent{action}}
}

func MapAgentGatewaySpan(span *tracev1.Span) MappedEvents {
	if span == nil {
		return MappedEvents{}
	}

	attrs := attributeMap(span.Attributes)
	mapped := MappedEvents{}

	action := NewObservedActionEvent()
	action.Timestamp = timestampFromUnixNano(span.StartTimeUnixNano)
	action.TraceID = hex.EncodeToString(span.TraceId)
	action.SpanID = hex.EncodeToString(span.SpanId)
	action.SessionKey = firstNonEmpty(attrs["mcp.session.id"], attrs["mcp.session_id"])
	action.MethodName = firstNonEmpty(attrs["mcp.method.name"], attrs["mcp.method"])
	action.ToolName = firstNonEmpty(attrs["gen_ai.tool.name"], attrs["mcp.tool.name"], attrs["mcp.resource.name"])
	action.Target = attrs["mcp.target"]
	action.ResourceType = attrs["mcp.resource.type"]
	action.ResourceURI = attrs["mcp.resource.uri"]
	action.GenAI = extractGenAIUsage(attrs)
	actionable := hasActionIdentity(action)
	if actionable {
		mapped.Actions = append(mapped.Actions, action)
	}

	if actionable && isOutcomeSpan(span, attrs) {
		outcome := NewObservedOutcomeEvent()
		outcome.Timestamp = timestampFromUnixNano(span.EndTimeUnixNano)
		outcome.TraceID = hex.EncodeToString(span.TraceId)
		outcome.SpanID = hex.EncodeToString(span.SpanId)
		outcome.SessionKey = action.SessionKey
		outcome.MethodName = action.MethodName
		outcome.ToolName = action.ToolName
		outcome.Target = action.Target
		outcome.Status = firstNonEmpty(attrs["http.status"], attrs["http.status_code"], attrs["otel.status_code"])
		outcome.ErrorCode = attrs["mcp.error.code"]
		outcome.ErrorMessage = firstNonEmpty(attrs["mcp.error.message"], attrs["reason"])
		outcome.GenAI = extractGenAIUsage(attrs)
		if span.Status != nil {
			switch span.Status.Code {
			case tracev1.Status_STATUS_CODE_OK:
				if outcome.Status == "" {
					outcome.Status = "200"
				}
			case tracev1.Status_STATUS_CODE_ERROR:
				if outcome.ErrorCode == "" {
					outcome.ErrorCode = "span_error"
				}
				if outcome.ErrorMessage == "" {
					outcome.ErrorMessage = span.Status.Message
				}
			}
		}
		mapped.Outcomes = append(mapped.Outcomes, outcome)
	}

	return mapped
}

func attributeMap(attributes []*commonv1.KeyValue) map[string]string {
	values := make(map[string]string, len(attributes))
	for _, attribute := range attributes {
		if attribute == nil || attribute.Value == nil {
			continue
		}
		values[attribute.Key] = anyValueString(attribute.Value)
	}
	return values
}

func anyValueString(value *commonv1.AnyValue) string {
	switch v := value.Value.(type) {
	case *commonv1.AnyValue_StringValue:
		return v.StringValue
	case *commonv1.AnyValue_BoolValue:
		if v.BoolValue {
			return "true"
		}
		return "false"
	case *commonv1.AnyValue_IntValue:
		return strconv.FormatInt(v.IntValue, 10)
	default:
		return ""
	}
}

func isOutcomeRecord(attrs map[string]string) bool {
	return attrs["http.status"] != "" || attrs["http.status_code"] != "" || attrs["otel.status_code"] != "" || attrs["mcp.error.code"] != ""
}

func isOutcomeSpan(span *tracev1.Span, attrs map[string]string) bool {
	if isOutcomeRecord(attrs) {
		return true
	}
	return span != nil && span.Status != nil && span.Status.Code != tracev1.Status_STATUS_CODE_UNSET
}

func hasActionIdentity(action ObservedActionEvent) bool {
	if action.MethodName != "tools/call" {
		return false
	}
	return firstNonEmpty(action.MethodName, action.ToolName, action.Target, action.ResourceType, action.ResourceURI) != ""
}

func timestampFromUnixNano(unixNano uint64) time.Time {
	if unixNano == 0 {
		return time.Time{}
	}
	return time.Unix(0, int64(unixNano)).UTC()
}

func extractGenAIUsage(attrs map[string]string) GenAIUsage {
	return GenAIUsage{
		Model:            firstNonEmpty(attrs["gen_ai.response.model"], attrs["gen_ai.request.model"]),
		PromptTokens:     attrs["gen_ai.usage.prompt_tokens"],
		CompletionTokens: attrs["gen_ai.usage.completion_tokens"],
		TotalTokens:      attrs["gen_ai.usage.total_tokens"],
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
