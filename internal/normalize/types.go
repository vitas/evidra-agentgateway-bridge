package normalize

import "time"

const (
	DefaultFlavor       = "imperative"
	DefaultEvidenceKind = "observed"
	DefaultSourceSystem = "agentgateway"
)

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
