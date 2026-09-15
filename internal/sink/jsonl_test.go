package sink

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vitas/evidra-agentgateway-bridge/internal/observation"
)

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

func TestJSONLWriteReturnsAnUnderlyingFileFailure(t *testing.T) {
	s, err := NewJSONL(filepath.Join(t.TempDir(), "observations.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(context.Background(), testExecution()); err == nil || !strings.Contains(err.Error(), "write execution") {
		t.Fatalf("write error = %v, want a wrapped write failure", err)
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
