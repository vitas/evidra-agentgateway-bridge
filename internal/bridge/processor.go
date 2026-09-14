// Package bridge turns normalized observations into records for a sink.
//
// It owns two things and nothing else: joining the signals that describe one execution, and
// deciding when a joined record is complete enough to emit. It does not decide what an
// execution means, and it does not classify anything about the tool, its arguments or its
// result.
package bridge

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vitas/evidra-agentgateway-bridge/internal/normalize"
	"github.com/vitas/evidra-agentgateway-bridge/internal/observation"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
)

// Sink receives one normalized execution at a time. It is an interface so the writer can be
// swapped without touching correlation: today it appends JSONL for inspection and parity
// runs, and the Evidence v2 writer lands behind the same signature.
type Sink interface {
	Write(ctx context.Context, ex observation.Execution) error
}

// Stats counts what the join actually did. It exists because a receiver that silently drops
// or silently half-merges looks identical to one that works: the numbers are the only way to
// tell them apart from the outside.
type Stats struct {
	LogRecordsSeen   int `json:"log_records_seen"`
	SpansSeen        int `json:"spans_seen"`
	ToolCallsIgnored int `json:"non_tool_call_ignored"`
	Emitted          int `json:"emitted"`
	EmittedMerged    int `json:"emitted_merged"`
	EmittedLogOnly   int `json:"emitted_log_only"`
	EmittedSpanOnly  int `json:"emitted_span_only"`
	UnjoinableNoSpan int `json:"unjoinable_missing_span_id"`
	Correlated       int `json:"correlated"`
	Unattributed     int `json:"unattributed"`
	Ambiguous        int `json:"ambiguous"`
	StillPending     int `json:"still_pending"`
}

type pending struct {
	ex        observation.Execution
	firstSeen time.Time
}

// Processor joins log records and spans on their shared trace and span id.
//
// The join key requires both. AgentGateway sets trace and span context on the OTLP log
// record it exports, so one request's log and span carry the same pair; joining on trace id
// alone would be a heuristic, because a single trace normally contains several tool calls and
// an operation id would land on whichever execution merged first.
type Processor struct {
	sink    Sink
	maxWait time.Duration
	now     func() time.Time

	mu              sync.Mutex
	observerID      string
	observerVersion string
	pending         map[string]pending
	stats           Stats
}

// NewProcessor returns a processor that emits to sink. maxWait bounds how long a record seen
// on only one signal waits for the other before being emitted as partial; zero means partial
// records are held until Flush.
func NewProcessor(sink Sink, maxWait time.Duration) *Processor {
	return &Processor{
		sink:    sink,
		maxWait: maxWait,
		now:     time.Now,
		pending: map[string]pending{},
	}
}

// SetObserver stamps an observer identity onto every execution this processor emits. It is set
// once at startup: two receivers writing to one store must be distinguishable afterwards, and
// "the gateway observed this" is not enough when there are two gateways.
func (p *Processor) SetObserver(id, version string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.observerID = id
	p.observerVersion = version
}

func (p *Processor) ConsumeLogRecords(ctx context.Context, records []*logsv1.LogRecord) error {
	if p == nil {
		return nil
	}
	for _, record := range records {
		p.count(func(s *Stats) { s.LogRecordsSeen++ })
		ex, ok := normalize.FromLogRecord(record)
		if !ok {
			p.count(func(s *Stats) { s.ToolCallsIgnored++ })
			continue
		}
		if err := p.ingest(ctx, ex); err != nil {
			return err
		}
	}
	return p.evictExpired(ctx)
}

func (p *Processor) ConsumeSpans(ctx context.Context, spans []*tracev1.Span) error {
	if p == nil {
		return nil
	}
	for _, span := range spans {
		p.count(func(s *Stats) { s.SpansSeen++ })
		ex, ok := normalize.FromSpan(span)
		if !ok {
			p.count(func(s *Stats) { s.ToolCallsIgnored++ })
			continue
		}
		if err := p.ingest(ctx, ex); err != nil {
			return err
		}
	}
	return p.evictExpired(ctx)
}

// Flush emits everything still waiting for a second signal. A run that ends without it leaves
// the single-signal executions unreported, which is the same shape as a receiver that dropped
// them.
func (p *Processor) Flush(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	keys := make([]string, 0, len(p.pending))
	for k := range p.pending {
		keys = append(keys, k)
	}
	p.mu.Unlock()
	sort.Strings(keys)
	for _, k := range keys {
		if err := p.emitKey(ctx, k); err != nil {
			return err
		}
	}
	return nil
}

func (p *Processor) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.stats
	s.StillPending = len(p.pending)
	return s
}

func (p *Processor) count(fn func(*Stats)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fn(&p.stats)
}

func (p *Processor) ingest(ctx context.Context, ex observation.Execution) error {
	key := observation.JoinKey(ex.TraceID, ex.SpanID)
	if key == "" {
		// No span id means nothing to join on. Emitting it anyway would put a record in the
		// store that can never be corroborated; dropping it silently would hide a source that
		// stopped setting trace context. Count it and drop it, and let the number be visible.
		p.count(func(s *Stats) { s.UnjoinableNoSpan++ })
		return nil
	}

	p.mu.Lock()
	if p.observerID != "" {
		ex.Source.ObserverID = p.observerID
		ex.Source.ObserverVersion = p.observerVersion
	}
	prev, exists := p.pending[key]
	if !exists {
		p.pending[key] = pending{ex: ex, firstSeen: p.now()}
		p.mu.Unlock()
		return nil
	}
	merged := merge(prev.ex, ex)
	delete(p.pending, key)
	p.mu.Unlock()
	return p.emit(ctx, merged, true)
}

func (p *Processor) evictExpired(ctx context.Context) error {
	if p.maxWait <= 0 {
		return nil
	}
	cutoff := p.now().Add(-p.maxWait)
	p.mu.Lock()
	var keys []string
	for k, v := range p.pending {
		if v.firstSeen.Before(cutoff) {
			keys = append(keys, k)
		}
	}
	p.mu.Unlock()
	sort.Strings(keys)
	for _, k := range keys {
		if err := p.emitKey(ctx, k); err != nil {
			return err
		}
	}
	return nil
}

func (p *Processor) emitKey(ctx context.Context, key string) error {
	p.mu.Lock()
	item, ok := p.pending[key]
	if ok {
		delete(p.pending, key)
	}
	p.mu.Unlock()
	if !ok {
		return nil
	}
	return p.emit(ctx, item.ex, false)
}

func (p *Processor) emit(ctx context.Context, ex observation.Execution, merged bool) error {
	switch ex.Correlation {
	case observation.Correlated:
		p.count(func(s *Stats) { s.Correlated++ })
	case observation.Ambiguous:
		p.count(func(s *Stats) { s.Ambiguous++ })
	default:
		p.count(func(s *Stats) { s.Unattributed++ })
	}
	p.count(func(s *Stats) {
		s.Emitted++
		switch {
		case merged:
			s.EmittedMerged++
		case len(ex.SeenFrom) == 1 && ex.SeenFrom[0] == normalize.SignalLogs:
			s.EmittedLogOnly++
		default:
			s.EmittedSpanOnly++
		}
	})
	if p.sink == nil {
		return nil
	}
	if err := p.sink.Write(ctx, ex); err != nil {
		return fmt.Errorf("sink: %w", err)
	}
	return nil
}

// merge combines two records of one execution. Field by field it prefers a value over an
// absence, and where both signals assert something different it keeps one and records the
// objection rather than choosing silently.
func merge(base, add observation.Execution) observation.Execution {
	out := base
	out.SeenFrom = union(base.SeenFrom, add.SeenFrom)
	dis := append([]string{}, base.Disagreements...)

	if out.TraceID == "" {
		out.TraceID = add.TraceID
	}
	if out.ParentSpanID == "" {
		out.ParentSpanID = add.ParentSpanID
	}
	out.Tool = mergeField("tool", base.Tool, add.Tool, &dis)
	out.Target = mergeField("target", base.Target, add.Target, &dis)

	if out.StartedAt.IsZero() {
		out.StartedAt = add.StartedAt
	}
	if out.FinishedAt.IsZero() {
		out.FinishedAt = add.FinishedAt
	}
	if out.DurationMS == 0 {
		out.DurationMS = add.DurationMS
	}
	if out.DurationMS == 0 && !out.StartedAt.IsZero() && !out.FinishedAt.IsZero() {
		out.DurationMS = out.FinishedAt.Sub(out.StartedAt).Milliseconds()
	}

	out.Status, out.ErrorType, out.ErrorCode = mergeStatus(base, add, &dis)
	out.Correlation, out.OperationID, out.CorrelationDetail = mergeCorrelation(base, add)
	out.ArgumentsFingerprintStatus = strongerAvailability(base.ArgumentsFingerprintStatus, add.ArgumentsFingerprintStatus)
	out.ResultFingerprintStatus = strongerAvailability(base.ResultFingerprintStatus, add.ResultFingerprintStatus)

	if out.ReadOnlyAnnotationKnown && !add.ReadOnlyAnnotationKnown {
		// keep base
	} else if add.ReadOnlyAnnotationKnown {
		out.ReadOnlyDeclared = add.ReadOnlyDeclared
		out.ReadOnlyAnnotationKnown = true
	}
	out.Source = mergeSource(base.Source, add.Source)
	if len(base.SeenFrom) > 0 && len(add.SeenFrom) > 0 {
		out.Source.Transport = "otlp_logs+otlp_traces"
	}
	out.Disagreements = dis
	return out
}

func mergeStatus(base, add observation.Execution, dis *[]string) (observation.Status, string, string) {
	a, b := base.Status, add.Status
	errType := firstNonEmpty(base.ErrorType, add.ErrorType)
	errCode := firstNonEmpty(base.ErrorCode, add.ErrorCode)

	if a == b {
		return a, errType, errCode
	}
	if a == observation.StatusUnknown {
		return b, errType, errCode
	}
	if b == observation.StatusUnknown {
		return a, errType, errCode
	}
	// Both asserted something and they differ. Keep the failure: an evidence recorder that
	// averaged a reported error away into a success would be worse than one that never saw it.
	*dis = append(*dis, fmt.Sprintf("status: %s signal said %s, %s signal said %s",
		strings.Join(base.SeenFrom, "+"), a, strings.Join(add.SeenFrom, "+"), b))
	if a == observation.StatusError || b == observation.StatusError {
		return observation.StatusError, errType, errCode
	}
	if a == observation.StatusCancelled || b == observation.StatusCancelled {
		return observation.StatusCancelled, errType, errCode
	}
	return a, errType, errCode
}

// mergeCorrelation re-classifies from both signals rather than preferring one. An id that was
// ambiguous on one signal cannot be rescued by the other being silent, and two different valid
// ids must stay a conflict instead of becoming whichever arrived first.
func mergeCorrelation(base, add observation.Execution) (observation.Correlation, string, string) {
	if base.Correlation == observation.Ambiguous || add.Correlation == observation.Ambiguous {
		detail := firstNonEmpty(base.CorrelationDetail, add.CorrelationDetail)
		if base.Correlation == observation.Ambiguous && add.Correlation == observation.Ambiguous {
			detail = base.CorrelationDetail + "; " + add.CorrelationDetail
		}
		return observation.Ambiguous, "", detail
	}
	return observation.ClassifyOperation(base.OperationID, add.OperationID)
}

// strongerAvailability keeps the more informative statement. That a source offered raw content
// and the bridge declined is a stronger claim than that the source emitted nothing, and
// dropping it during a merge would erase exactly the evidence a privacy canary looks for.
func strongerAvailability(a, b observation.Availability) observation.Availability {
	rank := func(v observation.Availability) int {
		switch v {
		case observation.RefusedRaw:
			return 4
		case observation.OmittedSize:
			return 3
		case observation.Present:
			return 2
		case observation.Absent:
			return 1
		default:
			return 0
		}
	}
	if rank(a) >= rank(b) {
		return a
	}
	return b
}

func mergeSource(a, b observation.Source) observation.Source {
	out := a
	if out.ObserverID == "" {
		out.ObserverID = b.ObserverID
	}
	if out.ObserverVersion == "" {
		out.ObserverVersion = b.ObserverVersion
	}
	if out.ServiceName == "" {
		out.ServiceName = b.ServiceName
	}
	if out.ProtocolVersion == "" {
		out.ProtocolVersion = b.ProtocolVersion
	}
	if out.NetworkTransport == "" {
		out.NetworkTransport = b.NetworkTransport
	}
	if out.SessionID == "" {
		out.SessionID = b.SessionID
	}
	return out
}

func mergeField(name, a, b string, dis *[]string) string {
	if a == b || b == "" {
		return a
	}
	if a == "" {
		return b
	}
	*dis = append(*dis, fmt.Sprintf("%s: signals disagreed (%q vs %q)", name, a, b))
	return a
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range append(append([]string{}, a...), b...) {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
