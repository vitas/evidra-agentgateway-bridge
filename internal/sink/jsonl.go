// Package sink writes normalized observations somewhere durable.
//
// The JSONL writer here is the first sink, and it is deliberately not a placeholder: it is
// what parity runs and the CORR-0 assertions read, so it is a supported output rather than a
// stub awaiting the real one.
package sink

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/vitas/evidra-agentgateway-bridge/internal/observation"
)

// JSONL appends one execution per line. It is append-only and never rewrites what it wrote,
// because a record that changed after the fact is not evidence.
type JSONL struct {
	mu   sync.Mutex
	path string
	f    *os.File
	w    *bufio.Writer
}

// NewJSONL opens or creates the file at path. Directories are created, so a run can point at
// a fresh output directory without a separate mkdir step.
func NewJSONL(path string) (*JSONL, error) {
	if path == "" {
		return nil, fmt.Errorf("sink path is required")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create sink directory: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open sink: %w", err)
	}
	return &JSONL{path: path, f: f, w: bufio.NewWriter(f)}, nil
}

func (s *JSONL) Write(ctx context.Context, ex observation.Execution) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("write execution: %w", err)
	}
	raw, err := json.Marshal(ex)
	if err != nil {
		return fmt.Errorf("marshal execution: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.w.Write(append(raw, '\n')); err != nil {
		return fmt.Errorf("write execution: %w", err)
	}
	// Flush per record. A receiver that buffers and is killed loses the tail, and the tail is
	// exactly the part a run that ended badly needs.
	if err := s.w.Flush(); err != nil {
		return fmt.Errorf("write execution: %w", err)
	}
	return nil
}

// Close flushes and closes the underlying file.
func (s *JSONL) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.w.Flush(); err != nil {
		_ = s.f.Close()
		return err
	}
	return s.f.Close()
}

// ReadAll parses a JSONL artifact back into executions. It exists so tests and parity runs
// read what was written rather than what the writer intended to write.
func ReadAll(path string) ([]observation.Execution, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []observation.Execution
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		if len(sc.Bytes()) == 0 {
			continue
		}
		var ex observation.Execution
		if err := json.Unmarshal(sc.Bytes(), &ex); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", path, line, err)
		}
		out = append(out, ex)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan sink %s: %w", path, err)
	}
	return out, nil
}
