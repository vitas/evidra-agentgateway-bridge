package bridge

import (
	"context"
	"encoding/hex"
	"errors"
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

type failOnceSink struct {
	attempts int
	written  []observation.Execution
}

type blockingSink struct {
	started chan struct{}
	release chan struct{}
	written []observation.Execution
}

func (s *blockingSink) Write(_ context.Context, ex observation.Execution) error {
	if len(s.written) == 0 {
		close(s.started)
		<-s.release
	}
	s.written = append(s.written, ex)
	return nil
}

type blockingFailOnceSink struct {
	started  chan struct{}
	release  chan struct{}
	attempts int
	written  []observation.Execution
}

func (s *blockingFailOnceSink) Write(_ context.Context, ex observation.Execution) error {
	s.attempts++
	if s.attempts == 1 {
		close(s.started)
		<-s.release
		return errors.New("injected sink failure")
	}
	s.written = append(s.written, ex)
	return nil
}

func (s *failOnceSink) Write(_ context.Context, ex observation.Execution) error {
	s.attempts++
	if s.attempts == 1 {
		return errors.New("disk unavailable")
	}
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

	if err := p.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sink.written) != 1 {
		t.Fatalf("wrote %d executions, want exactly 1", len(sink.written))
	}
	if err := p.Flush(ctx); err != nil {
		t.Fatal(err)
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
	if stats.EmittedMerged != 1 || stats.Correlated != 1 || stats.StillBuffered != 0 {
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
	if err := p.Flush(ctx); err != nil {
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
	if err := p.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sink.written) != 1 {
		t.Fatalf("wrote %d, want 1", len(sink.written))
	}
	if err := p.Flush(ctx); err != nil {
		t.Fatal(err)
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
	if err := p.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sink.written) != 2 {
		t.Fatalf("wrote %d executions, want 2", len(sink.written))
	}
	if err := p.Flush(ctx); err != nil {
		t.Fatal(err)
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
	if err := p.Flush(ctx); err != nil {
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
	if err := p.Flush(ctx); err != nil {
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
	if p.Stats().StillBuffered != 1 {
		t.Errorf("stats = %+v, want 1 pending", p.Stats())
	}
	if err := p.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sink.written) != 1 {
		t.Fatalf("flush wrote %d, want 1", len(sink.written))
	}
	if err := p.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	ex := sink.written[0]
	if strings.Join(ex.SeenFrom, ",") != "logs" {
		t.Errorf("seen_from = %v, want logs only - a partial must not look merged", ex.SeenFrom)
	}
	if ex.Correlation != observation.Correlated || ex.OperationID != opA {
		t.Errorf("correlation lost on a partial: %+v", ex)
	}
}

func TestFlushRetainsAnExecutionUntilTheSinkPersistsIt(t *testing.T) {
	sink := &failOnceSink{}
	p := NewProcessor(sink, 0)
	ctx := context.Background()

	if err := p.ConsumeLogRecords(ctx, []*logsv1.LogRecord{
		gatewayLog(t, traceA, spanA, "restart", "alpha", opA),
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.Flush(ctx); err == nil || !strings.Contains(err.Error(), "sink: disk unavailable") {
		t.Fatalf("first flush error = %v, want wrapped sink failure", err)
	}
	if stats := p.Stats(); stats.Emitted != 0 || stats.EmittedLogOnly != 0 || stats.Correlated != 0 || stats.SinkWriteFailures != 1 || stats.StillBuffered != 1 {
		t.Fatalf("stats after failed persistence = %+v", stats)
	}

	if err := p.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if stats := p.Stats(); stats.Emitted != 1 || stats.EmittedLogOnly != 1 || stats.Correlated != 1 || stats.SinkWriteFailures != 1 || stats.StillBuffered != 0 {
		t.Fatalf("stats after retry = %+v", stats)
	}
	if sink.attempts != 2 || len(sink.written) != 1 {
		t.Fatalf("sink attempts/writes = %d/%d, want 2/1", sink.attempts, len(sink.written))
	}
}

func TestFailedFlushMergesAConcurrentComplementBeforeRetry(t *testing.T) {
	sink := &blockingFailOnceSink{started: make(chan struct{}), release: make(chan struct{})}
	p := NewProcessor(sink, 0)
	ctx := context.Background()
	if err := p.ConsumeSpans(ctx, []*tracev1.Span{
		gatewaySpan(t, traceA, spanA, "restart", "alpha", "", tracev1.Status_STATUS_CODE_UNSET),
	}); err != nil {
		t.Fatal(err)
	}

	flushed := make(chan error, 1)
	go func() { flushed <- p.Flush(ctx) }()
	<-sink.started
	if err := p.ConsumeLogRecords(ctx, []*logsv1.LogRecord{
		gatewayLog(t, traceA, spanA, "restart", "alpha", opA),
	}); err != nil {
		t.Fatal(err)
	}
	if stats := p.Stats(); stats.StillBuffered != 1 || stats.LateSignalsIgnored != 0 {
		t.Fatalf("stats while one join key has active and pending signals = %+v", stats)
	}
	close(sink.release)
	if err := <-flushed; err == nil || !strings.Contains(err.Error(), "injected sink failure") {
		t.Fatalf("first flush error = %v", err)
	}
	if stats := p.Stats(); stats.Emitted != 0 || stats.StillBuffered != 1 || stats.LateSignalsIgnored != 0 {
		t.Fatalf("stats after failed generation = %+v", stats)
	}

	if err := p.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sink.written) != 1 {
		t.Fatalf("persisted %d records, want one merged retry", len(sink.written))
	}
	ex := sink.written[0]
	if ex.OperationID != opA || ex.Correlation != observation.Correlated || strings.Join(ex.SeenFrom, ",") != "logs,traces" {
		t.Fatalf("retried execution did not merge its complementary signal: %+v", ex)
	}
	if stats := p.Stats(); stats.Emitted != 1 || stats.EmittedMerged != 1 || stats.StillBuffered != 0 {
		t.Fatalf("final stats = %+v", stats)
	}
}

func TestFailedBatchMergesPendingSignalsForUnattemptedKeys(t *testing.T) {
	sink := &blockingFailOnceSink{started: make(chan struct{}), release: make(chan struct{})}
	p := NewProcessor(sink, 0)
	ctx := context.Background()
	if err := p.ConsumeSpans(ctx, []*tracev1.Span{
		gatewaySpan(t, traceA, spanA, "restart", "alpha", "", tracev1.Status_STATUS_CODE_UNSET),
		gatewaySpan(t, traceB, spanB, "apply_yaml", "beta", "", tracev1.Status_STATUS_CODE_UNSET),
	}); err != nil {
		t.Fatal(err)
	}

	flushed := make(chan error, 1)
	go func() { flushed <- p.Flush(ctx) }()
	<-sink.started
	if err := p.ConsumeLogRecords(ctx, []*logsv1.LogRecord{
		gatewayLog(t, traceB, spanB, "apply_yaml", "beta", opB),
	}); err != nil {
		t.Fatal(err)
	}
	close(sink.release)
	if err := <-flushed; err == nil {
		t.Fatal("first batch flush succeeded")
	}
	if stats := p.Stats(); stats.Emitted != 0 || stats.StillBuffered != 2 {
		t.Fatalf("stats after failed batch = %+v", stats)
	}

	if err := p.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sink.written) != 2 {
		t.Fatalf("persisted %d records on retry, want one per join key", len(sink.written))
	}
	byTrace := map[string]observation.Execution{}
	for _, ex := range sink.written {
		byTrace[ex.TraceID] = ex
	}
	if ex := byTrace[traceB]; ex.OperationID != opB || strings.Join(ex.SeenFrom, ",") != "logs,traces" {
		t.Fatalf("unattempted key lost its pending complement: %+v", ex)
	}
	if stats := p.Stats(); stats.Emitted != 2 || stats.EmittedMerged != 1 || stats.StillBuffered != 0 || stats.LateSignalsIgnored != 0 {
		t.Fatalf("final stats after retried batch = %+v", stats)
	}
}

func TestSuccessfulFlushFinalizesTheJoinKeyAndCountsConcurrentLateSignals(t *testing.T) {
	sink := &blockingSink{started: make(chan struct{}), release: make(chan struct{})}
	p := NewProcessor(sink, 0)
	ctx := context.Background()
	if err := p.ConsumeSpans(ctx, []*tracev1.Span{
		gatewaySpan(t, traceA, spanA, "restart", "alpha", "", tracev1.Status_STATUS_CODE_UNSET),
	}); err != nil {
		t.Fatal(err)
	}

	flushed := make(chan error, 1)
	go func() { flushed <- p.Flush(ctx) }()
	<-sink.started
	// This is a legitimate complementary signal for the same execution. The snapshot already
	// being persisted defines finalization, so the late signal must be counted and dropped.
	if err := p.ConsumeLogRecords(ctx, []*logsv1.LogRecord{
		gatewayLog(t, traceA, spanA, "restart", "alpha", opA),
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.ConsumeSpans(ctx, []*tracev1.Span{
		gatewaySpan(t, traceA, spanA, "restart", "alpha", "", tracev1.Status_STATUS_CODE_UNSET),
		gatewaySpan(t, traceA, spanA, "restart", "alpha", "", tracev1.Status_STATUS_CODE_UNSET),
	}); err != nil {
		t.Fatal(err)
	}
	close(sink.release)
	if err := <-flushed; err != nil {
		t.Fatal(err)
	}
	if stats := p.Stats(); stats.Emitted != 1 || stats.StillBuffered != 0 || stats.LateSignalsIgnored != 3 {
		t.Fatalf("stats after finalization = %+v", stats)
	}

	if err := p.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sink.written) != 1 {
		t.Fatalf("persisted %d records for one join key, want 1", len(sink.written))
	}
}

func TestSignalAfterFinalizationIsIgnoredAndCountedOnce(t *testing.T) {
	sink := &recordingSink{}
	p := NewProcessor(sink, 0)
	ctx := context.Background()
	span := gatewaySpan(t, traceA, spanA, "restart", "alpha", "", tracev1.Status_STATUS_CODE_UNSET)
	if err := p.ConsumeSpans(ctx, []*tracev1.Span{span}); err != nil {
		t.Fatal(err)
	}
	if err := p.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.ConsumeSpans(ctx, []*tracev1.Span{span}); err != nil {
		t.Fatal(err)
	}
	if err := p.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sink.written) != 1 {
		t.Fatalf("persisted %d records for one join key, want 1", len(sink.written))
	}
	stats := p.Stats()
	if stats.SpansSeen != 2 || stats.LateSignalsIgnored != 1 || stats.StillBuffered != 0 {
		t.Fatalf("stats after post-finalization signal = %+v", stats)
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

// clientSpan and serverSpan reproduce the pair AgentGateway v1.5.0 emits for one tools/call,
// measured during BR-1: the client span is the outbound call to the upstream and is exported
// first, and the server span is the trace root that carries the CEL-projected operation id
// because the projection reads the inbound request headers.
func clientSpan(t *testing.T, traceID, spanID, parentID, tool, target string) *tracev1.Span {
	t.Helper()
	s := gatewaySpan(t, traceID, spanID, tool, target, "", tracev1.Status_STATUS_CODE_UNSET)
	s.ParentSpanId = raw(t, parentID)
	s.Kind = tracev1.Span_SPAN_KIND_CLIENT
	return s
}

func serverSpan(t *testing.T, traceID, spanID, tool, target, opID string) *tracev1.Span {
	t.Helper()
	s := gatewaySpan(t, traceID, spanID, tool, target, opID, tracev1.Status_STATUS_CODE_UNSET)
	s.Kind = tracev1.Span_SPAN_KIND_SERVER
	return s
}

// TestOneToolCallIsOneExecution is the BR-1 defect. One tools/call through AgentGateway emits a
// client span and a server span, both carrying mcp.method.name and gen_ai.tool.name, so a
// receiver that normalizes per span reports two executions for one call. Deduplicating on
// tool+target+time would be the heuristic the correlation contract forbids; the parent link is
// trace structure the source asserted, so that is what is used.
func TestOneToolCallIsOneExecution(t *testing.T) {
	const (
		serverSpanID = "ffffffffffffffff"
		clientSpanID = "eeeeeeeeeeeeeeee"
	)
	for _, tc := range []struct {
		name  string
		first func(t *testing.T) *tracev1.Span
		then  func(t *testing.T) *tracev1.Span
	}{
		{
			// The order actually observed: the client span ends first, so it is exported first.
			name: "client span arrives before its parent",
			first: func(t *testing.T) *tracev1.Span {
				return clientSpan(t, traceA, clientSpanID, serverSpanID, "restart", "alpha")
			},
			then: func(t *testing.T) *tracev1.Span { return serverSpan(t, traceA, serverSpanID, "restart", "alpha", opA) },
		},
		{
			name:  "server span arrives first",
			first: func(t *testing.T) *tracev1.Span { return serverSpan(t, traceA, serverSpanID, "restart", "alpha", opA) },
			then: func(t *testing.T) *tracev1.Span {
				return clientSpan(t, traceA, clientSpanID, serverSpanID, "restart", "alpha")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &recordingSink{}
			p := NewProcessor(sink, 0)
			ctx := context.Background()

			if err := p.ConsumeSpans(ctx, []*tracev1.Span{tc.first(t)}); err != nil {
				t.Fatal(err)
			}
			if err := p.ConsumeSpans(ctx, []*tracev1.Span{tc.then(t)}); err != nil {
				t.Fatal(err)
			}
			if err := p.Flush(ctx); err != nil {
				t.Fatal(err)
			}

			if len(sink.written) != 1 {
				t.Fatalf("one tools/call produced %d executions: %+v", len(sink.written), sink.written)
			}
			if err := p.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			ex := sink.written[0]
			// The correlation lives on the parent; the execution facts on the child. Losing
			// either half makes the record useless in a different way.
			if ex.Correlation != observation.Correlated || ex.OperationID != opA {
				t.Errorf("correlation = %s/%q, want the parent span's id adopted (%s)", ex.Correlation, ex.OperationID, opA)
			}
			if ex.Tool != "restart" || ex.Target != "alpha" {
				t.Errorf("tool/target = %q/%q", ex.Tool, ex.Target)
			}
			if ex.SpanID != clientSpanID {
				t.Errorf("span = %q, want the client span %q - the outbound call is the execution", ex.SpanID, clientSpanID)
			}
			if ex.SpanKind != "client" {
				t.Errorf("span kind = %q, want client", ex.SpanKind)
			}
		})
	}
}

// TestSingleSpanDeploymentStillProducesARecord guards the other side of the dedup rule: a
// gateway that emits one span per call, with no client/server pair, must not have its execution
// suppressed as somebody's parent.
func TestSingleSpanDeploymentStillProducesARecord(t *testing.T) {
	sink := &recordingSink{}
	p := NewProcessor(sink, 0)
	ctx := context.Background()

	s := gatewaySpan(t, traceA, spanA, "restart", "alpha", opA, tracev1.Status_STATUS_CODE_UNSET)
	s.Kind = tracev1.Span_SPAN_KIND_INTERNAL
	if err := p.ConsumeSpans(ctx, []*tracev1.Span{s}); err != nil {
		t.Fatal(err)
	}
	if err := p.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sink.written) != 1 {
		t.Fatalf("wrote %d, want 1", len(sink.written))
	}
	if sink.written[0].Correlation != observation.Correlated {
		t.Errorf("correlation = %s", sink.written[0].Correlation)
	}
}

// TestMergingTwoSpansDoesNotClaimALogItNeverSaw covers a reporting bug found while fixing the
// duplicate: the merged transport was hardcoded to "otlp_logs+otlp_traces", so two spans of one
// signal produced a record claiming an access log had contributed to it.
func TestMergingTwoSpansDoesNotClaimALogItNeverSaw(t *testing.T) {
	sink := &recordingSink{}
	p := NewProcessor(sink, 0)
	ctx := context.Background()

	if err := p.ConsumeSpans(ctx, []*tracev1.Span{
		clientSpan(t, traceA, "eeeeeeeeeeeeeeee", "ffffffffffffffff", "restart", "alpha"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.ConsumeSpans(ctx, []*tracev1.Span{
		serverSpan(t, traceA, "ffffffffffffffff", "restart", "alpha", opA),
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sink.written) != 1 {
		t.Fatalf("wrote %d, want 1", len(sink.written))
	}
	if err := p.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	ex := sink.written[0]
	if ex.Source.Transport != "otlp_traces" {
		t.Errorf("transport = %q, want otlp_traces - no access log was seen", ex.Source.Transport)
	}
	if strings.Join(ex.SeenFrom, ",") != "traces" {
		t.Errorf("seen_from = %v, want [traces]", ex.SeenFrom)
	}
}

// TestLogArrivingBeforeTheClientSpanStillMerges is the ordering BR-1 actually observed. The
// access log carries the server span's id, so it is buffered under the parent's key; the client
// span that represents the execution arrives afterwards and claims that parent. A receiver that
// only redirects records arriving after the claim exists emits the log as a second execution,
// which is what the first BR-1 run produced: seven records for six calls.
func TestLogArrivingBeforeTheClientSpanStillMerges(t *testing.T) {
	const (
		serverSpanID = "ffffffffffffffff"
		clientSpanID = "eeeeeeeeeeeeeeee"
	)
	sink := &recordingSink{}
	p := NewProcessor(sink, 0)
	ctx := context.Background()

	if err := p.ConsumeLogRecords(ctx, []*logsv1.LogRecord{
		gatewayLog(t, traceA, serverSpanID, "restart", "alpha", opA),
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.ConsumeSpans(ctx, []*tracev1.Span{
		clientSpan(t, traceA, clientSpanID, serverSpanID, "restart", "alpha"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.ConsumeSpans(ctx, []*tracev1.Span{
		serverSpan(t, traceA, serverSpanID, "restart", "alpha", opA),
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	if len(sink.written) != 1 {
		t.Fatalf("one tools/call produced %d executions: %+v", len(sink.written), sink.written)
	}
	if err := p.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	ex := sink.written[0]
	if ex.Correlation != observation.Correlated || ex.OperationID != opA {
		t.Errorf("correlation = %s/%q", ex.Correlation, ex.OperationID)
	}
	if len(ex.SeenFrom) != 2 {
		t.Errorf("seen_from = %v, want the log and the spans merged", ex.SeenFrom)
	}
	if stats := p.Stats(); stats.StillBuffered != 0 {
		t.Errorf("stats = %+v", stats)
	}
}
