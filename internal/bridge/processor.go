// Package bridge turns normalized observations into records for a sink.
//
// It owns two things and nothing else: assembling the signals that describe one execution, and
// deciding when an assembly is final. It does not decide what an execution means, and it does
// not classify anything about the tool, its arguments or its result.
//
// Assembly happens in one pass over buffered signals rather than incrementally as each signal
// arrives. That is a deliberate choice made after an incremental version was measured against a
// real AgentGateway: the gateway exports spans when they end and access logs when the request
// completes, and the two are not ordered with respect to each other. An incremental receiver has
// to guess whether a record is complete, and every ordering it did not anticipate produced a
// second record for one tool call - measured at 7 and then 8 records for 6 calls. Buffering and
// assembling once removes the ordering question entirely, at the cost of a bounded delay.
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
// swapped without touching assembly: today it appends JSONL for inspection and parity runs, and
// the Evidence v2 writer lands behind the same signature.
type Sink interface {
	Write(ctx context.Context, ex observation.Execution) error
}

// Stats counts what the assembly actually did. It exists because a receiver that silently drops
// or silently half-assembles looks identical from outside to one that works.
type Stats struct {
	LogRecordsSeen     int `json:"log_records_seen"`
	SpansSeen          int `json:"spans_seen"`
	ToolCallsIgnored   int `json:"non_tool_call_ignored"`
	Emitted            int `json:"emitted"`
	EmittedMerged      int `json:"emitted_merged"`
	EmittedLogOnly     int `json:"emitted_log_only"`
	EmittedSpanOnly    int `json:"emitted_span_only"`
	SinkWriteFailures  int `json:"sink_write_failures"`
	LateSignalsIgnored int `json:"late_signals_ignored"`
	UnjoinableNoSpan   int `json:"unjoinable_missing_span_id"`
	// ParentsSuppressed counts spans not emitted because a client span in the same trace claimed
	// them as parent. One tools/call is two spans; without this the receiver reports twice the
	// executions that happened, which reads as coverage rather than as duplication.
	ParentsSuppressed int `json:"parent_spans_suppressed"`
	Correlated        int `json:"correlated"`
	Unattributed      int `json:"unattributed"`
	Ambiguous         int `json:"ambiguous"`
	StillBuffered     int `json:"still_buffered"`
}

// Processor buffers normalized signals and assembles executions from them.
type Processor struct {
	sink    Sink
	maxWait time.Duration
	now     func() time.Time

	assembleMu       sync.Mutex
	mu               sync.Mutex
	observerID       string
	observerVersion  string
	spans            map[string]observation.Execution
	logs             map[string]observation.Execution
	firstSeen        map[string]time.Time
	pendingSpans     map[string]observation.Execution
	pendingLogs      map[string]observation.Execution
	pendingFirstSeen map[string]time.Time
	pendingSignals   map[string]int
	inFlight         map[string]bool
	emitted          map[string]bool
	stats            Stats
}

type assembledExecution struct {
	key               string
	consumedKeys      []string
	ex                observation.Execution
	both              bool
	loggy             bool
	parentsSuppressed int
}

// NewProcessor returns a processor that emits to sink. maxWait is how long a signal is held
// waiting for the rest of its execution before assembly runs without it; zero assembles only on
// Flush, which is what makes the tests deterministic.
func NewProcessor(sink Sink, maxWait time.Duration) *Processor {
	return &Processor{
		sink:             sink,
		maxWait:          maxWait,
		now:              time.Now,
		spans:            map[string]observation.Execution{},
		logs:             map[string]observation.Execution{},
		firstSeen:        map[string]time.Time{},
		pendingSpans:     map[string]observation.Execution{},
		pendingLogs:      map[string]observation.Execution{},
		pendingFirstSeen: map[string]time.Time{},
		pendingSignals:   map[string]int{},
		inFlight:         map[string]bool{},
		emitted:          map[string]bool{},
	}
}

// SetObserver stamps an observer identity onto every execution this processor emits. It is set
// once at startup: two receivers writing one store must be distinguishable afterwards, and "the
// gateway observed this" is not enough when there are two gateways.
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
		p.buffer(ex, normalize.SignalLogs)
	}
	return p.assembleExpired(ctx)
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
		p.buffer(ex, normalize.SignalTraces)
	}
	return p.assembleExpired(ctx)
}

// Flush assembles everything still buffered. A run that ends without it reports a coverage gap
// that was really a lifecycle bug.
func (p *Processor) Flush(ctx context.Context) error {
	return p.assemble(ctx, time.Time{})
}

func (p *Processor) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.stats
	s.StillBuffered = len(p.firstSeen)
	for key := range p.pendingFirstSeen {
		if _, active := p.firstSeen[key]; !active {
			s.StillBuffered++
		}
	}
	return s
}

func (p *Processor) count(fn func(*Stats)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fn(&p.stats)
}

func (p *Processor) buffer(ex observation.Execution, signal string) {
	key := observation.JoinKey(ex.TraceID, ex.SpanID)
	if key == "" {
		// Nothing to assemble on. Emitting it would put a record in the store that can never be
		// corroborated; dropping it quietly would hide a source that stopped setting trace
		// context. Count it and drop it, and let the number be visible.
		p.count(func(s *Stats) { s.UnjoinableNoSpan++ })
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.emitted[key] {
		// This is a normalized tool-call signal, but its execution key is already durable.
		// Count it as late rather than as a non-tool call, and never reopen the key.
		p.stats.LateSignalsIgnored++
		return
	}
	if p.observerID != "" {
		ex.Source.ObserverID = p.observerID
		ex.Source.ObserverVersion = p.observerVersion
	}
	spans, logs, firstSeen := p.spans, p.logs, p.firstSeen
	usePending := p.inFlight[key] || p.hasPending(key)
	if usePending {
		spans, logs, firstSeen = p.pendingSpans, p.pendingLogs, p.pendingFirstSeen
	}
	dst := spans
	if signal == normalize.SignalLogs {
		dst = logs
	}
	if prev, ok := dst[key]; ok {
		ex = merge(prev, ex)
	}
	dst[key] = ex
	if _, ok := firstSeen[key]; !ok {
		firstSeen[key] = p.now()
	}
	if usePending {
		p.pendingSignals[key]++
	}
}

func (p *Processor) hasPending(key string) bool {
	_, ok := p.pendingFirstSeen[key]
	return ok
}

func (p *Processor) assembleExpired(ctx context.Context) error {
	if p.maxWait <= 0 {
		return nil
	}
	return p.assemble(ctx, p.now().Add(-p.maxWait))
}

// assemble builds executions from the buffered signals and writes them. A zero cutoff assembles
// everything, which is what Flush does; otherwise only keys first seen before the cutoff are
// assembled, so signals still in flight get a chance to join.
//
// The rule for what counts as one execution, measured against AgentGateway v1.5.0 in BR-1:
//
//	one tools/call  ->  a server span: the gateway's inbound handling, the trace root, and the
//	                    carrier of the CEL-projected operation id, because the projection reads
//	                    the inbound request headers
//	                ->  a client span: the outbound call to the upstream, parented to it
//	                ->  one access-log record, carrying the server span's id
//
// The execution is the client span, because that is the call that reached the upstream. The
// server span is suppressed and contributes its correlation; the access log follows whichever
// span carries its id, and its contribution ends up on the client span. Deduplicating on
// tool+target+time instead would be exactly the heuristic the correlation contract forbids; the
// parent link is trace structure the source asserted.
func (p *Processor) assemble(ctx context.Context, cutoff time.Time) error {
	// Only one assembly may claim buffered records at a time. The processor mutex is still
	// released before sink I/O, so ingestion and Stats remain responsive while persistence is
	// in progress; serializing assemblers prevents two concurrent Flush calls from writing the
	// same retained snapshot.
	p.assembleMu.Lock()
	defer p.assembleMu.Unlock()

	p.mu.Lock()
	ready := p.readyKeys(cutoff)
	if len(ready) == 0 {
		p.mu.Unlock()
		return nil
	}

	// childOf maps a parent span key to the client span naming it as parent, across all buffered
	// spans and not only the ready ones: a parent may age out while its child is still inside the
	// wait, and suppressing the parent is correct either way.
	childOf := map[string]string{}
	for key, span := range p.spans {
		if span.SpanKind == normalize.SpanKindClient && span.ParentSpanID != "" {
			childOf[span.TraceID+"/"+span.ParentSpanID] = key
		}
	}

	var out []assembledExecution

	for _, key := range ready {
		// A span or log keyed by a parent span id that a client span claims is not an
		// execution of its own. The access log carries the server span's id, so it lands
		// under the parent's key; the execution is the client span, and the log's data
		// belongs there. An incremental receiver missed this because the log arrived before
		// the claim existed; assembling once over all buffered signals removes the ordering
		// question entirely.
		if childKey, claimed := childOf[key]; claimed && childKey != key {
			// The child claims this signal when the child itself is assembled. Until that
			// record is durably written, leave every contributing signal buffered.
			continue
		}

		if a, ok := p.assembleKey(key); ok {
			out = append(out, a)
		}
	}
	for _, a := range out {
		for _, key := range a.consumedKeys {
			p.inFlight[key] = true
		}
	}
	p.mu.Unlock()

	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	for i, a := range out {
		if err := p.emit(ctx, a); err != nil {
			p.restoreReserved(out[i:])
			return err
		}
	}
	return nil
}

// restoreReserved rolls every failed or unattempted snapshot back into active assembly. Signals
// received during sink I/O live in pending maps; folding them back here makes the retry observe
// one merged generation per JoinKey.
func (p *Processor) restoreReserved(records []assembledExecution) {
	p.mu.Lock()
	defer p.mu.Unlock()
	restored := map[string]bool{}
	for _, a := range records {
		for _, key := range a.consumedKeys {
			if restored[key] {
				continue
			}
			restored[key] = true
			p.mergePendingIntoActive(key)
			delete(p.inFlight, key)
		}
	}
}

// assembleKey snapshots one execution while p.mu is held. It does not mutate buffered state;
// the caller acknowledges every contributing key only after persistence succeeds.
func (p *Processor) assembleKey(key string) (assembledExecution, bool) {
	span, hasSpan := p.spans[key]
	logRec, hasLog := p.logs[key]
	if !hasSpan && !hasLog {
		return assembledExecution{}, false
	}
	if !hasSpan {
		return assembledExecution{
			key:          key,
			consumedKeys: []string{key},
			ex:           logRec,
			loggy:        true,
		}, true
	}

	a := assembledExecution{
		key:          key,
		consumedKeys: []string{key},
		ex:           span,
		both:         hasLog,
	}
	// A client span takes its correlation from its parent, which is where the projected
	// operation id lands.
	if span.SpanKind == normalize.SpanKindClient && span.ParentSpanID != "" {
		p.mergeParent(&a, span.TraceID+"/"+span.ParentSpanID)
	}
	if hasLog {
		a.ex = merge(a.ex, logRec)
	}
	return a, true
}

func (p *Processor) mergeParent(a *assembledExecution, parentKey string) {
	parent, hasParentSpan := p.spans[parentKey]
	parentLog, hasParentLog := p.logs[parentKey]
	if hasParentSpan {
		a.ex = merge(a.ex, parent)
		a.parentsSuppressed = 1
	}
	if hasParentLog {
		a.ex = merge(a.ex, parentLog)
	}
	if hasParentSpan || hasParentLog {
		a.consumedKeys = append(a.consumedKeys, parentKey)
	}
}

// readyKeys returns buffered keys whose wait has elapsed, in a stable order.
func (p *Processor) readyKeys(cutoff time.Time) []string {
	keys := make([]string, 0, len(p.firstSeen))
	for key, seen := range p.firstSeen {
		if cutoff.IsZero() || seen.Before(cutoff) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func (p *Processor) emit(ctx context.Context, a assembledExecution) error {
	p.mu.Lock()
	if p.emitted[a.key] {
		p.mu.Unlock()
		return nil
	}
	sink := p.sink
	p.mu.Unlock()

	if sink != nil {
		if err := sink.Write(ctx, a.ex); err != nil {
			p.mu.Lock()
			p.stats.SinkWriteFailures++
			p.mu.Unlock()
			return fmt.Errorf("sink: %w", err)
		}
	}

	// Persistence succeeded. Only now may the buffered inputs be acknowledged and the
	// success counters advance.
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, key := range a.consumedKeys {
		delete(p.spans, key)
		delete(p.logs, key)
		delete(p.firstSeen, key)
		delete(p.inFlight, key)
		p.stats.LateSignalsIgnored += p.discardPending(key)
		p.emitted[key] = true
	}
	p.stats.ParentsSuppressed += a.parentsSuppressed
	switch a.ex.Correlation {
	case observation.Correlated:
		p.stats.Correlated++
	case observation.Ambiguous:
		p.stats.Ambiguous++
	default:
		p.stats.Unattributed++
	}
	p.stats.Emitted++
	switch {
	case a.both || len(a.ex.SeenFrom) > 1:
		p.stats.EmittedMerged++
	case a.loggy:
		p.stats.EmittedLogOnly++
	default:
		p.stats.EmittedSpanOnly++
	}
	return nil
}

// mergePendingIntoActive restores signals received during a failed in-flight write. It is called
// with p.mu held and preserves the original first-seen time when the active generation exists.
func (p *Processor) mergePendingIntoActive(key string) {
	seen, ok := p.pendingFirstSeen[key]
	if !ok {
		return
	}
	if ex, exists := p.pendingSpans[key]; exists {
		if active, activeExists := p.spans[key]; activeExists {
			p.spans[key] = merge(active, ex)
		} else {
			p.spans[key] = ex
		}
		delete(p.pendingSpans, key)
	}
	if ex, exists := p.pendingLogs[key]; exists {
		if active, activeExists := p.logs[key]; activeExists {
			p.logs[key] = merge(active, ex)
		} else {
			p.logs[key] = ex
		}
		delete(p.pendingLogs, key)
	}
	if _, active := p.firstSeen[key]; !active {
		p.firstSeen[key] = seen
	}
	delete(p.pendingFirstSeen, key)
	delete(p.pendingSignals, key)
}

// discardPending drops signals that arrived after a snapshot began persistence. A successful
// write finalizes every contributing JoinKey, so reopening one would create a second record for
// the same execution. The return value is the number of actual signals dropped.
func (p *Processor) discardPending(key string) int {
	dropped := p.pendingSignals[key]
	delete(p.pendingSpans, key)
	delete(p.pendingLogs, key)
	delete(p.pendingFirstSeen, key)
	delete(p.pendingSignals, key)
	return dropped
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
	if out.SpanID == "" {
		out.SpanID = add.SpanID
	}
	if out.ParentSpanID == "" {
		out.ParentSpanID = add.ParentSpanID
	}
	// A log record carries no span kind. Taking the span's is what lets assembly tell an
	// execution from the gateway's own handling of the request that caused it.
	if out.SpanKind == "" {
		out.SpanKind = add.SpanKind
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

	if add.ReadOnlyAnnotationKnown {
		out.ReadOnlyDeclared = add.ReadOnlyDeclared
		out.ReadOnlyAnnotationKnown = true
	}
	out.Source = mergeSource(base.Source, add.Source)
	out.Source.Transport = transportOf(out.SeenFrom)
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
		if base.Correlation == observation.Ambiguous && add.Correlation == observation.Ambiguous &&
			base.CorrelationDetail != add.CorrelationDetail {
			detail = base.CorrelationDetail + "; " + add.CorrelationDetail
		}
		return observation.Ambiguous, "", detail
	}
	return observation.ClassifyOperation(base.OperationID, add.OperationID)
}

// strongerAvailability keeps the more informative statement. That a source offered raw content
// and the bridge declined is a stronger claim than that the source emitted nothing, and dropping
// it during a merge would erase exactly the evidence a privacy canary looks for.
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

// transportOf names the signals a record was built from. Deriving it rather than hardcoding a
// two-signal value is what keeps a merged pair of spans from claiming it saw an access log.
func transportOf(seenFrom []string) string {
	switch len(seenFrom) {
	case 0:
		return ""
	case 1:
		return "otlp_" + seenFrom[0]
	default:
		parts := make([]string, 0, len(seenFrom))
		for _, s := range seenFrom {
			parts = append(parts, "otlp_"+s)
		}
		return strings.Join(parts, "+")
	}
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
