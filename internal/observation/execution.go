// Package observation holds the normalized record of one observed MCP tool execution.
//
// It is the adapter boundary the AgentGateway/OTel plan calls ObservedExecution: a single
// record carrying a start, an end and a status, rather than an action/outcome pair that has
// to be re-joined afterwards by a heuristic key. The pair shape was the origin of the old
// bridge's correlation bug - once "the call" and "its result" are separate events, something
// has to decide they belong together, and that something ended up being
// session+trace+method+tool+target, none of which is authoritative.
//
// Nothing in this package knows about AgentGateway attribute names or about OTLP. Both live
// behind it: normalize maps telemetry into Execution, and the sinks consume Execution.
package observation

import (
	"regexp"
	"strings"
	"time"
)

// Correlation states how an execution came to be attached to an operation. Every execution
// gets exactly one value; there is no fourth "probably" state, and nothing may move an
// execution out of Unattributed by reasoning about time, tool name or target.
type Correlation string

const (
	// Correlated means exactly one explicit, well-formed operation id was present.
	Correlated Correlation = "correlated"
	// Unattributed means no explicit operation id was present at all. This is an honest
	// empty, not a failure: an agent that never prescribed produces only these.
	Unattributed Correlation = "unattributed"
	// Ambiguous means correlation metadata was present but unusable - conflicting ids, or
	// an id that is not well formed. It is kept separate from Unattributed because the two
	// have different causes and different fixes.
	Ambiguous Correlation = "ambiguous"
)

// Status is the terminal state of an execution, in the vocabulary the evidence model
// already uses. It is derived from protocol signals only - a JSON-RPC error object, the
// tool result's isError flag, or a cancellation - never from an HTTP status range and never
// from the shape of an argument.
type Status string

const (
	StatusSuccess   Status = "success"
	StatusError     Status = "error"
	StatusCancelled Status = "cancelled"
	StatusUnknown   Status = "unknown"
)

// Availability says why a fingerprint is or is not present. The distinction matters: a
// payload the gateway never emitted and a payload Evidra refused to carry are different
// facts, and collapsing them into one empty string would make a privacy bound
// indistinguishable from a telemetry gap.
type Availability string

const (
	Present     Availability = "present"
	Absent      Availability = "absent"
	NotEmitted  Availability = "not_emitted_by_source"
	OmittedSize Availability = "omitted_oversize"
	// RefusedRaw means the source did offer the raw payload and the bridge declined to
	// carry it. It is distinct from NotEmitted on purpose: a gateway configured to export
	// `gen_ai.tool.call.arguments` produces this, and a privacy canary has to be able to tell
	// "policy held" apart from "there was nothing to hold it against".
	RefusedRaw Availability = "refused_raw_present_at_source"
)

// Source identifies who observed the execution. The evidence model's provenance says the
// record came from an observer rather than from the agent; these fields say which observer,
// so a reader can tell a gateway-observed execution from a proxy-observed one without
// inferring it from the shape of the data.
type Source struct {
	ObserverType     string `json:"observer_type"`
	ObserverID       string `json:"observer_id,omitempty"`
	ObserverVersion  string `json:"observer_version,omitempty"`
	Transport        string `json:"transport"`
	ServiceName      string `json:"service_name,omitempty"`
	ProtocolVersion  string `json:"protocol_version,omitempty"`
	NetworkTransport string `json:"network_transport,omitempty"`
	// SessionID is diagnostic only. MCP 2026-07-28 removed the protocol-level session, and
	// even where a gateway still issues one it is never authoritative for operation
	// membership. It is recorded because it helps a human line things up, and marked here
	// so no future reader mistakes it for a correlation key.
	SessionID string `json:"session_id_diagnostic,omitempty"`
}

// Execution is one observed tool call.
type Execution struct {
	TraceID      string `json:"trace_id,omitempty"`
	SpanID       string `json:"span_id,omitempty"`
	ParentSpanID string `json:"parent_span_id,omitempty"`

	// OperationID is only ever set from an explicit correlation signal. It stays empty when
	// Correlation is not Correlated, so an empty id and a missing attribution cannot drift
	// apart.
	OperationID string      `json:"operation_id,omitempty"`
	Correlation Correlation `json:"correlation"`
	// CorrelationDetail records what was found, so an ambiguous execution can be diagnosed
	// without re-deriving it: the conflicting values, or why an id was rejected.
	CorrelationDetail string `json:"correlation_detail,omitempty"`

	Target string `json:"target,omitempty"`
	Tool   string `json:"tool,omitempty"`

	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	DurationMS int64     `json:"duration_ms,omitempty"`

	Status    Status `json:"status"`
	ErrorType string `json:"error_type,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`

	// Fingerprints are HMACs computed by the evidence writer, never raw payloads. This
	// struct deliberately has no field capable of holding tool arguments or results, so a
	// raw payload cannot reach a sink by accident - only by someone adding a field, which
	// is a visible decision.
	ArgumentsFingerprint       string       `json:"arguments_fingerprint,omitempty"`
	ArgumentsFingerprintStatus Availability `json:"arguments_fingerprint_status"`
	ResultFingerprint          string       `json:"result_fingerprint,omitempty"`
	ResultFingerprintStatus    Availability `json:"result_fingerprint_status"`

	// ReadOnlyDeclared is the upstream server's own tools/list annotation. Absent is not
	// false: a server that declared nothing must not be rendered as a server that declared
	// read-only, which is why the presence flag is carried beside it.
	ReadOnlyDeclared        bool `json:"read_only_declared"`
	ReadOnlyAnnotationKnown bool `json:"read_only_annotation_known"`
	// AnnotationsVerified is always false for gateway-observed executions: the gateway
	// relays the server's declaration, it does not verify it.
	AnnotationsVerified bool `json:"annotations_verified"`

	Source Source `json:"source"`

	// SeenFrom records which signals contributed to this record, because a merged execution
	// is stronger evidence than one seen only in a log or only in a span, and a reader
	// should not have to guess which happened.
	SeenFrom []string `json:"seen_from,omitempty"`

	// Disagreements records where the contributing signals contradicted each other, in words.
	// A log that says success and a span that says error is not resolved by picking one; the
	// record keeps the chosen value and says the other signal objected. An evidence layer that
	// silently reconciles its own inputs cannot be audited by anyone reading its output.
	Disagreements []string `json:"disagreements,omitempty"`
}

// JoinKey is the identity used to merge a log record with a span. It is exact by
// construction: AgentGateway sets both trace and span context on the exported OTLP log
// record, so the two signals for one request carry the same pair.
//
// A span id is required. Joining on trace id alone would be a heuristic - one trace
// normally contains several tool calls - and would silently attribute an operation id to
// whichever execution happened to merge first.
func JoinKey(traceID, spanID string) string {
	traceID = strings.TrimSpace(traceID)
	spanID = strings.TrimSpace(spanID)
	if traceID == "" || spanID == "" {
		return ""
	}
	return traceID + "/" + spanID
}

// operationIDPattern is the shape Evidra's own operation ids have: the `EV-` prefix plus a
// 26-character Crockford base32 ULID. It is checked so that a malformed correlation value
// lands in Ambiguous rather than being accepted as an operation nobody issued.
var operationIDPattern = regexp.MustCompile(`^EV-[0-9A-HJKMNP-TV-Z]{26}$`)

// ValidOperationID reports whether a correlation value could be an Evidra operation id.
func ValidOperationID(id string) bool {
	return operationIDPattern.MatchString(strings.TrimSpace(id))
}

// ClassifyOperation turns the operation ids found on the contributing signals into exactly
// one Correlation value plus the id and a detail string for the artifact.
//
// The rules are ordered so that the honest answer wins: nothing found is Unattributed, one
// well-formed value agreed on by every signal that carried one is Correlated, and anything
// else - a conflict, or a value that is not an operation id - is Ambiguous. An id is never
// repaired, completed from another field, or inferred.
func ClassifyOperation(found ...string) (Correlation, string, string) {
	var present []string
	for _, id := range found {
		if id = strings.TrimSpace(id); id != "" {
			present = append(present, id)
		}
	}
	if len(present) == 0 {
		return Unattributed, "", "no explicit operation id on any contributing signal"
	}

	first := present[0]
	for _, id := range present[1:] {
		if id != first {
			return Ambiguous, "", "conflicting operation ids: " + strings.Join(present, " vs ")
		}
	}
	if !ValidOperationID(first) {
		return Ambiguous, "", "operation id is not well formed: " + first
	}
	return Correlated, first, ""
}
