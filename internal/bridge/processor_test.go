package bridge

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/vitas/evidra-agentgateway-bridge/internal/observation"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
)

const (
	traceA = "4bf92f3577b34da6a3ce929d0e0e4736"
	spanA  = "00f067aa0ba902b7"
	traceB = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	spanB  = "bbbbbbbbbbbbbbbb"
	opA    = "EV-01M2FAS00J36FTV4EG5EJC1D7S"
	opB    = "EV-01M2EB52K0000000000000000A"
)

type recordingSink struct {
	written []observation.Execution
}

func (s *recordingSink) Write(_ context.Context, ex observation.Execution) error {
	s.written = append(s.written, ex)
	return nil
}

func kv(k, v string) *commonv1.KeyValue {
	return &commonv1.KeyValue{Key: k, Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: v}}}
}

func raw(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// gatewayLog is an AgentGateway access-log record for a tools/call, shaped like the one
// captured from v1.5.0 during CORR-0: the CEL-projected operation id rides beside
// mcp.target and gen_ai.tool.name in the same record.
func gatewayLog(t *testing.T, traceID, spanID, tool, target, opID string, extra ...*commonv1.KeyValue) *logsv1.LogRecord {
	t.Helper()
	attrs := []*commonv1.KeyValue{
		kv("mcp.method.name", "tools/call"),
		kv("mcp.target", target),
		kv("mcp.resource.type", "tool"),
		kv("gen_ai.tool.name", tool),
		kv("http.status", "200"),
		kv("duration", "6"),
	}
	if opID != "" {
		attrs = append(attrs, kv("evidra_op", opID))
	}
	attrs = append(attrs, extra...)
	return &logsv1.LogRecord{
		TraceId:      raw(t, traceID),
		SpanId:       raw(t, spanID),
		TimeUnixNano: 1757872938000000000,
		Attributes:   attrs,
	}
}

func gatewaySpan(t *testing.T, traceID, spanID, tool, target, opID string, status tracev1.Status_StatusCode, extra ...*commonv1.KeyValue) *tracev1.Span {
	t.Helper()
	attrs := []*commonv1.KeyValue{
		kv("gen_ai.operation.name", "execute_tool"),
		kv("mcp.method.name", "tools/call"),
		kv("mcp.target", target),
		kv("gen_ai.tool.name", tool),
	}
	if opID != "" {
		attrs = append(attrs, kv("evidra_op", opID))
	}
	attrs = append(attrs, extra...)
	return &tracev1.Span{
		TraceId:           raw(t, traceID),
		SpanId:            raw(t, spanID),
		Name:              "tools/call " + tool,
		StartTimeUnixNano: 1757872938000000000,
		EndTimeUnixNano:   1757872938006000000,
		Status:            &tracev1.Status{Code: status},
		Attributes:        attrs,
	}
}

func TestLogAndSpanMergeIntoOneExecution(t *testing.T) {
	sink := &recordingSink{}
	p := NewProcessor(sink, 0)
	ctx := context.Background()

	// The operation id only ever reaches telemetry through the access-log projection; the
	// execution facts come from the span. Neither signal alone is a usable record.
	if err := p.ConsumeLogRecords(ctx, []*logsv1.LogRecord{gatewayLog(t, traceA, spanA, "restart", "alpha", opA)}); err != nil {
		t.Fatal(err)
	}
	if len(sink.written) != 0 {
		t.Fatalf("emitted before the second signal arrived: %+v", sink.written)
	}
	if err := p.ConsumeSpans(ctx, []*tracev1.Span{gatewaySpan(t, traceA, spanA, "restart", "alpha", "", tracev1.Status_STATUS_CODE_UNSET)}); err != nil {
		t.Fatal(err)
	}

	if len(sink.written) != 1 {
		t.Fatalf("wrote %d executions, want exactly 1", len(sink.written))
	}
	ex := sink.written[0]
	if ex.OperationID != opA || ex.Correlation != observation.Correlated {
		t.Errorf("correlation = %s/%q, want correlated/%s", ex.Correlation, ex.OperationID, opA)
	}
	if ex.Tool != "restart" || ex.Target != "alpha" {
		t.Errorf("tool/target = %q/%q", ex.Tool, ex.Target)
	}
	if ex.Status != observation.StatusSuccess {
		t.Errorf("status = %s, want success", ex.Status)
	}
	// The span carries both ends; the log carried neither. Timing must come from whichever
	// signal has it.
	if ex.StartedAt.IsZero() || ex.FinishedAt.IsZero() || ex.DurationMS != 6 {
		t.Errorf("timing not merged: start=%v end=%v dur=%d", ex.StartedAt, ex.FinishedAt, ex.DurationMS)
	}
	if strings.Join(ex.SeenFrom, ",") != "logs,traces" {
		t.Errorf("seen_from = %v, want both signals", ex.SeenFrom)
	}
	if ex.Source.Transport != "otlp_logs+otlp_traces" {
		t.Errorf("transport = %q, want the merged value", ex.Source.Transport)
	}
	stats := p.Stats()
	if stats.EmittedMerged != 1 || stats.Correlated != 1 || stats.StillPending != 0 {
		t.Errorf("stats = %+v", stats)
	}
}

func TestMergeIsOrderIndependent(t *testing.T) {
	sink := &recordingSink{}
	p := NewProcessor(sink, 0)
	ctx := context.Background()

	// Spans and logs are exported on different schedules and arrive in either order.
	if err := p.ConsumeSpans(ctx, []*tracev1.Span{gatewaySpan(t, traceA, spanA, "restart", "alpha", "", tracev1.Status_STATUS_CODE_UNSET)}); err != nil {
		t.Fatal(err)
	}
	if err := p.ConsumeLogRecords(ctx, []*logsv1.LogRecord{gatewayLog(t, traceA, spanA, "restart", "alpha", opA)}); err != nil {
		t.Fatal(err)
	}
	if len(sink.written) != 1 {
		t.Fatalf("wrote %d, want 1", len(sink.written))
	}
	if sink.written[0].OperationID != opA || sink.written[0].Correlation != observation.Correlated {
		t.Errorf("correlation lost when the span arrived first: %+v", sink.written[0])
	}
}

func TestConflictingOperationIDsBecomeAmbiguous(t *testing.T) {
	sink := &recordingSink{}
	p := NewProcessor(sink, 0)
	ctx := context.Background()

	// Two valid ids for one execution. Picking the first by arrival order would silently
	// attribute someone else's work; the record must stay ambiguous and say why.
	if err := p.ConsumeLogRecords(ctx, []*logsv1.LogRecord{gatewayLog(t, traceA, spanA, "restart", "alpha", opA)}); err != nil {
		t.Fatal(err)
	}
	if err := p.ConsumeSpans(ctx, []*tracev1.Span{gatewaySpan(t, traceA, spanA, "restart", "alpha", opB, tracev1.Status_STATUS_CODE_UNSET)}); err != nil {
		t.Fatal(err)
	}
	if len(sink.written) != 1 {
		t.Fatalf("wrote %d, want 1", len(sink.written))
	}
	ex := sink.written[0]
	if ex.Correlation != observation.Ambiguous {
		t.Fatalf("correlation = %s, want ambiguous", ex.Correlation)
	}
	if ex.OperationID != "" {
		t.Errorf("an ambiguous execution must not carry an operation id, got %q", ex.OperationID)
	}
	if !strings.Contains(ex.CorrelationDetail, "conflicting") {
		t.Errorf("detail does not explain the ambiguity: %q", ex.CorrelationDetail)
	}
	if p.Stats().Ambiguous != 1 {
		t.Errorf("stats = %+v", p.Stats())
	}
}

func TestNoSpanIDIsCountedNotSilentlyDropped(t *testing.T) {
	sink := &recordingSink{}
	p := NewProcessor(sink, 0)
	ctx := context.Background()

	// Nothing to join on. Emitting it would put an uncorroborated record in the store;
	// dropping it quietly would hide a source that stopped setting trace context.
	rec := gatewayLog(t, traceA, spanA, "restart", "alpha", opA)
	rec.SpanId = nil
	rec.Attributes = append(rec.Attributes, kv("span.id", ""))
	if err := p.ConsumeLogRecords(ctx, []*logsv1.LogRecord{rec}); err != nil {
		t.Fatal(err)
	}
	if len(sink.written) != 0 {
		t.Fatalf("emitted a record with no join key: %+v", sink.written)
	}
	if p.Stats().UnjoinableNoSpan != 1 {
		t.Errorf("stats = %+v, want the drop to be counted", p.Stats())
	}
}

func TestConcurrentOperationsDoNotCrossCorrelate(t *testing.T) {
	sink := &recordingSink{}
	p := NewProcessor(sink, 0)
	ctx := context.Background()

	// CORR-0 assertion 6, at the receiver: two operations in flight, each with its own trace
	// and span, interleaved across both signals.
	if err := p.ConsumeLogRecords(ctx, []*logsv1.LogRecord{
		gatewayLog(t, traceA, spanA, "restart", "alpha", opA),
		gatewayLog(t, traceB, spanB, "apply_yaml", "beta", opB),
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.ConsumeSpans(ctx, []*tracev1.Span{
		gatewaySpan(t, traceB, spanB, "apply_yaml", "beta", "", tracev1.Status_STATUS_CODE_UNSET),
		gatewaySpan(t, traceA, spanA, "restart", "alpha", "", tracev1.Status_STATUS_CODE_UNSET),
	}); err != nil {
		t.Fatal(err)
	}
	if len(sink.written) != 2 {
		t.Fatalf("wrote %d executions, want 2", len(sink.written))
	}
	byOp := map[string]observation.Execution{}
	for _, ex := range sink.written {
		byOp[ex.OperationID] = ex
	}
	if a := byOp[opA]; a.Tool != "restart" || a.Target != "alpha" {
		t.Errorf("operation A got the wrong execution: %+v", a)
	}
	if b := byOp[opB]; b.Tool != "apply_yaml" || b.Target != "beta" {
		t.Errorf("operation B got the wrong execution: %+v", b)
	}
}

func TestStatusDisagreementKeepsTheFailureAndSaysSo(t *testing.T) {
	sink := &recordingSink{}
	p := NewProcessor(sink, 0)
	ctx := context.Background()

	if err := p.ConsumeLogRecords(ctx, []*logsv1.LogRecord{
		gatewayLog(t, traceA, spanA, "restart", "alpha", opA),
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.ConsumeSpans(ctx, []*tracev1.Span{
		gatewaySpan(t, traceA, spanA, "restart", "alpha", "", tracev1.Status_STATUS_CODE_ERROR,
			kv("error.type", "tool_error")),
	}); err != nil {
		t.Fatal(err)
	}
	ex := sink.written[0]
	if ex.Status != observation.StatusError {
		t.Errorf("status = %s, want error - a reported failure must not be averaged away by a success elsewhere", ex.Status)
	}
	if ex.ErrorType != "tool_error" {
		t.Errorf("error_type = %q", ex.ErrorType)
	}
	if len(ex.Disagreements) == 0 {
		t.Fatal("the disagreement was resolved silently; an evidence layer that reconciles its own inputs without saying so cannot be audited")
	}
	if !strings.Contains(strings.Join(ex.Disagreements, " "), "status") {
		t.Errorf("disagreement does not name the field: %v", ex.Disagreements)
	}
}

func TestRefusedRawPayloadSurvivesTheMerge(t *testing.T) {
	sink := &recordingSink{}
	p := NewProcessor(sink, 0)
	ctx := context.Background()

	if err := p.ConsumeLogRecords(ctx, []*logsv1.LogRecord{
		gatewayLog(t, traceA, spanA, "restart", "alpha", opA),
	}); err != nil {
		t.Fatal(err)
	}
	// The span was exported with content capture enabled. The bridge carries none of it, and
	// the merge must not downgrade "the source offered raw content and we declined" to "the
	// source emitted nothing" - that downgrade is exactly what would make a privacy canary
	// pass on a receiver that leaks.
	if err := p.ConsumeSpans(ctx, []*tracev1.Span{
		gatewaySpan(t, traceA, spanA, "restart", "alpha", "", tracev1.Status_STATUS_CODE_UNSET,
			kv("gen_ai.tool.call.arguments", `{"secret":"hunter2"}`)),
	}); err != nil {
		t.Fatal(err)
	}
	ex := sink.written[0]
	if ex.ArgumentsFingerprintStatus != observation.RefusedRaw {
		t.Errorf("arguments availability = %s, want refused_raw_present_at_source", ex.ArgumentsFingerprintStatus)
	}
	if strings.Contains(ex.Tool, "hunter2") || strings.Contains(ex.CorrelationDetail, "hunter2") {
		t.Error("raw payload content reached the record")
	}
}

func TestFlushEmitsSingleSignalPartials(t *testing.T) {
	sink := &recordingSink{}
	p := NewProcessor(sink, 0)
	ctx := context.Background()

	// Only the log ever arrived. With maxWait=0 the partial is held, so a run that ends
	// without a flush reports a coverage gap that was really a lifecycle bug.
	if err := p.ConsumeLogRecords(ctx, []*logsv1.LogRecord{
		gatewayLog(t, traceA, spanA, "restart", "alpha", opA),
	}); err != nil {
		t.Fatal(err)
	}
	if len(sink.written) != 0 {
		t.Fatal("emitted a partial before flush with maxWait=0")
	}
	if p.Stats().StillPending != 1 {
		t.Errorf("stats = %+v, want 1 pending", p.Stats())
	}
	if err := p.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sink.written) != 1 {
		t.Fatalf("flush wrote %d, want 1", len(sink.written))
	}
	ex := sink.written[0]
	if strings.Join(ex.SeenFrom, ",") != "logs" {
		t.Errorf("seen_from = %v, want logs only - a partial must not look merged", ex.SeenFrom)
	}
	if ex.Correlation != observation.Correlated || ex.OperationID != opA {
		t.Errorf("correlation lost on a partial: %+v", ex)
	}
}

func TestMaxWaitEmitsPartialsWithoutAnExplicitFlush(t *testing.T) {
	sink := &recordingSink{}
	p := NewProcessor(sink, time.Millisecond)
	base := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	current := base
	p.now = func() time.Time { return current }
	ctx := context.Background()

	if err := p.ConsumeLogRecords(ctx, []*logsv1.LogRecord{
		gatewayLog(t, traceA, spanA, "restart", "alpha", opA),
	}); err != nil {
		t.Fatal(err)
	}
	if len(sink.written) != 0 {
		t.Fatal("evicted before the wait elapsed")
	}
	current = base.Add(time.Second)
	// Any later ingest triggers eviction of what has aged out.
	if err := p.ConsumeLogRecords(ctx, []*logsv1.LogRecord{
		gatewayLog(t, traceB, spanB, "apply_yaml", "beta", opB),
	}); err != nil {
		t.Fatal(err)
	}
	if len(sink.written) != 1 {
		t.Fatalf("wrote %d after the wait elapsed, want 1", len(sink.written))
	}
	if sink.written[0].OperationID != opA {
		t.Errorf("evicted the wrong record: %+v", sink.written[0])
	}
}

func TestNonToolCallTelemetryIsIgnoredAndCounted(t *testing.T) {
	sink := &recordingSink{}
	p := NewProcessor(sink, 0)
	ctx := context.Background()

	logRec := gatewayLog(t, traceA, spanA, "restart", "alpha", opA)
	for i, a := range logRec.Attributes {
		if a.Key == "mcp.method.name" {
			logRec.Attributes[i] = kv("mcp.method.name", "tools/list")
		}
	}
	if err := p.ConsumeLogRecords(ctx, []*logsv1.LogRecord{logRec}); err != nil {
		t.Fatal(err)
	}
	if len(sink.written) != 0 {
		t.Fatalf("a tools/list record became an execution: %+v", sink.written)
	}
	if p.Stats().ToolCallsIgnored != 1 {
		t.Errorf("stats = %+v", p.Stats())
	}
}

func TestObserverIdentityIsStamped(t *testing.T) {
	sink := &recordingSink{}
	p := NewProcessor(sink, 0)
	p.SetObserver("bridge-test", "1.2.3")
	ctx := context.Background()

	if err := p.ConsumeLogRecords(ctx, []*logsv1.LogRecord{gatewayLog(t, traceA, spanA, "restart", "alpha", opA)}); err != nil {
		t.Fatal(err)
	}
	if err := p.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	src := sink.written[0].Source
	if src.ObserverID != "bridge-test" || src.ObserverVersion != "1.2.3" {
		t.Errorf("observer identity = %+v", src)
	}
	if src.ObserverType != "agentgateway_otlp" {
		t.Errorf("observer type = %q", src.ObserverType)
	}
}
