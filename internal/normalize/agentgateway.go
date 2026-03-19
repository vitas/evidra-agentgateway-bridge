package normalize

import (
	"encoding/hex"
	"strconv"
	"time"

	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
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
		outcome.SessionKey = attrs["mcp.session.id"]
		outcome.MethodName = attrs["mcp.method.name"]
		outcome.ToolName = firstNonEmpty(attrs["gen_ai.tool.name"], attrs["mcp.tool.name"])
		outcome.Target = attrs["mcp.target"]
		outcome.Status = firstNonEmpty(attrs["http.status"], attrs["otel.status_code"])
		outcome.ErrorCode = attrs["mcp.error.code"]
		outcome.ErrorMessage = firstNonEmpty(attrs["mcp.error.message"], attrs["reason"])
		return MappedEvents{Outcomes: []ObservedOutcomeEvent{outcome}}
	}

	action := NewObservedActionEvent()
	action.Timestamp = timestampFromUnixNano(record.TimeUnixNano)
	action.TraceID = hex.EncodeToString(record.TraceId)
	action.SpanID = hex.EncodeToString(record.SpanId)
	action.SessionKey = attrs["mcp.session.id"]
	action.MethodName = attrs["mcp.method.name"]
	action.ToolName = firstNonEmpty(attrs["gen_ai.tool.name"], attrs["mcp.tool.name"])
	action.Target = attrs["mcp.target"]
	action.ResourceType = attrs["mcp.resource.type"]
	action.ResourceURI = attrs["mcp.resource.uri"]
	return MappedEvents{Actions: []ObservedActionEvent{action}}
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
	return attrs["http.status"] != "" || attrs["otel.status_code"] != "" || attrs["mcp.error.code"] != ""
}

func timestampFromUnixNano(unixNano uint64) time.Time {
	if unixNano == 0 {
		return time.Time{}
	}
	return time.Unix(0, int64(unixNano)).UTC()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
