package normalize

import "testing"

func TestObservedActionEventDefaultsToImperativeObservedAgentGateway(t *testing.T) {
	ev := NewObservedActionEvent()
	if ev.Flavor != "imperative" || ev.EvidenceKind != "observed" || ev.SourceSystem != "agentgateway" {
		t.Fatalf("unexpected taxonomy: %#v", ev)
	}
}
