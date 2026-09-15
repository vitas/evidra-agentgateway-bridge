package sink

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vitas/evidra-agentgateway-bridge/internal/observation"
)

type partialOnceFile struct {
	jsonlFile
	writeCalls  int
	rollbackErr error
}

func (f *partialOnceFile) Write(p []byte) (int, error) {
	f.writeCalls++
	if f.writeCalls > 1 {
		return f.jsonlFile.Write(p)
	}
	n, err := f.jsonlFile.Write(p[:len(p)/2])
	if err != nil {
		return n, err
	}
	return n, errors.New("injected write failure")
}

func (f *partialOnceFile) Truncate(size int64) error {
	if f.rollbackErr != nil {
		return f.rollbackErr
	}
	return f.jsonlFile.Truncate(size)
}

type cancellationProbeContext struct {
	checked  chan struct{}
	once     sync.Once
	canceled atomic.Bool
}

func (c *cancellationProbeContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *cancellationProbeContext) Done() <-chan struct{}       { return nil }
func (c *cancellationProbeContext) Value(any) any               { return nil }
func (c *cancellationProbeContext) Err() error {
	c.once.Do(func() { close(c.checked) })
	if c.canceled.Load() {
		return context.Canceled
	}
	return nil
}

func testExecution() observation.Execution {
	return observation.Execution{
		TraceID:     "trace-1",
		SpanID:      "span-1",
		Tool:        "restart",
		Target:      "alpha",
		Status:      observation.StatusSuccess,
		Correlation: observation.Correlated,
		OperationID: "EV-01M2FAS00J36FTV4EG5EJC1D7S",
		SeenFrom:    []string{"logs", "traces"},
	}
}

func TestJSONLWriteSurvivesCloseAndReadBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "observations.jsonl")
	s, err := NewJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	want := testExecution()
	if err := s.Write(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := ReadAll(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].OperationID != want.OperationID || got[0].Tool != want.Tool {
		t.Fatalf("read back = %+v, want one persisted execution", got)
	}
}

func TestJSONLWriteReturnsAnUnderlyingFileStateFailure(t *testing.T) {
	s, err := NewJSONL(filepath.Join(t.TempDir(), "observations.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(context.Background(), testExecution()); err == nil || !strings.Contains(err.Error(), "stat sink") {
		t.Fatalf("write error = %v, want a wrapped file-state failure", err)
	}
}

func TestJSONLCloseReturnsAnUnderlyingFileFailure(t *testing.T) {
	s, err := NewJSONL(filepath.Join(t.TempDir(), "observations.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err == nil {
		t.Fatal("close succeeded after the underlying file was closed")
	}
}

func TestReadAllReportsAnOversizedRecordAsAScannerError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized.jsonl")
	line := strings.Repeat("x", 8*1024*1024+1)
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := ReadAll(path)
	if err == nil || !strings.Contains(err.Error(), "scan sink") || !strings.Contains(err.Error(), "token too long") {
		t.Fatalf("ReadAll error = %v, want a clear scanner-size failure", err)
	}
}

func TestNewJSONLCreatesAPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "observations.jsonl")
	s, err := NewJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("file mode = %04o, want 0600", got)
	}
}

func TestJSONLWriteRejectsAnAlreadyCancelledContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "observations.jsonl")
	s, err := NewJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := s.Write(ctx, testExecution()); err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("write error = %v, want context cancellation", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("cancelled write persisted %d bytes", len(data))
	}
}

func TestJSONLWriteRollsBackAPartialRecordAndCanRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "observations.jsonl")
	seed, err := NewJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Write(context.Background(), testExecution()); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := NewJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	fault := &partialOnceFile{jsonlFile: s.f}
	s.f = fault
	t.Cleanup(func() { _ = s.Close() })

	if err := s.Write(context.Background(), testExecution()); err == nil || !strings.Contains(err.Error(), "injected write failure") {
		t.Fatalf("first write error = %v", err)
	}
	if err := s.Write(context.Background(), testExecution()); err != nil {
		t.Fatalf("retry: %v", err)
	}
	got, err := ReadAll(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].OperationID != testExecution().OperationID || got[1].OperationID != testExecution().OperationID {
		t.Fatalf("read back after retry = %+v, want the existing record plus exactly one intact retry", got)
	}
}

func TestJSONLPoisonsItselfWhenPartialWriteCannotBeRolledBack(t *testing.T) {
	s, err := NewJSONL(filepath.Join(t.TempDir(), "observations.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	fault := &partialOnceFile{jsonlFile: s.f, rollbackErr: errors.New("injected rollback failure")}
	s.f = fault
	t.Cleanup(func() { _ = s.Close() })

	err = s.Write(context.Background(), testExecution())
	if err == nil || !strings.Contains(err.Error(), "injected write failure") || !strings.Contains(err.Error(), "injected rollback failure") {
		t.Fatalf("first write error = %v, want write and rollback failures", err)
	}
	err = s.Write(context.Background(), testExecution())
	if err == nil || !strings.Contains(err.Error(), "sink is poisoned") {
		t.Fatalf("second write error = %v, want terminal poisoned state", err)
	}
	if fault.writeCalls != 1 {
		t.Fatalf("poisoned sink attempted %d writes, want 1", fault.writeCalls)
	}
}

func TestJSONLWriteRechecksCancellationAfterWaitingForItsMutex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "observations.jsonl")
	s, err := NewJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := &cancellationProbeContext{checked: make(chan struct{})}

	s.mu.Lock()
	written := make(chan error, 1)
	go func() { written <- s.Write(ctx, testExecution()) }()
	<-ctx.checked
	ctx.canceled.Store(true)
	s.mu.Unlock()

	if err := <-written; !errors.Is(err, context.Canceled) {
		t.Fatalf("write error = %v, want cancellation after lock wait", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("cancelled write persisted %d bytes", len(data))
	}
}
