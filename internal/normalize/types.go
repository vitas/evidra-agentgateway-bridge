package normalize

import "time"

const (
	DefaultFlavor       = "imperative"
	DefaultEvidenceKind = "observed"
	DefaultSourceSystem = "agentgateway"
)

// GenAIUsage holds LLM efficiency metrics extracted from gen_ai.* OTLP attributes.
type GenAIUsage struct {
	Model            string
	PromptTokens     string
	CompletionTokens string
	TotalTokens      string
}

type ObservedActionEvent struct {
	Timestamp    time.Time
	TraceID      string
	SpanID       string
	SessionKey   string
	MethodName   string
	ToolName     string
	Target       string
	ResourceType string
	ResourceURI  string
	Flavor       string
	EvidenceKind string
	SourceSystem string
	GenAI        GenAIUsage
}

type ObservedOutcomeEvent struct {
	Timestamp    time.Time
	TraceID      string
	SpanID       string
	SessionKey   string
	MethodName   string
	ToolName     string
	Target       string
	Status       string
	ErrorCode    string
	ErrorMessage string
	Flavor       string
	EvidenceKind string
	SourceSystem string
	GenAI        GenAIUsage
}

func NewObservedActionEvent() ObservedActionEvent {
	return ObservedActionEvent{
		Flavor:       DefaultFlavor,
		EvidenceKind: DefaultEvidenceKind,
		SourceSystem: DefaultSourceSystem,
	}
}

func NewObservedOutcomeEvent() ObservedOutcomeEvent {
	return ObservedOutcomeEvent{
		Flavor:       DefaultFlavor,
		EvidenceKind: DefaultEvidenceKind,
		SourceSystem: DefaultSourceSystem,
	}
}
