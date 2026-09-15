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
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/vitas/evidra-agentgateway-bridge/internal/observation"
)

type jsonlFile interface {
	Write([]byte) (int, error)
	Stat() (os.FileInfo, error)
	Truncate(int64) error
	Close() error
}

// JSONL appends one execution per line. It is append-only and never rewrites a successful
// record, because a record that changed after the fact is not evidence. One JSONL instance owns
// its path exclusively until Close; concurrent processes or separately opened JSONL instances
// writing the same path are unsupported because transactional rollback uses the previous size.
type JSONL struct {
	mu       sync.Mutex
	path     string
	f        jsonlFile
	poisoned error
}

// NewJSONL opens or creates the file at path. Directories are created, so a run can point at
// a fresh output directory without a separate mkdir step. The caller must ensure no other writer
// uses the same path until this sink is closed.
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
	return &JSONL{path: path, f: f}, nil
}

// Write appends a complete JSON line. Success means the operating system accepted every byte;
// it does not promise fsync-level durability across a host crash. A failed or partial append is
// truncated back to its starting size so the caller can retry without leaving a corrupt prefix.
// If rollback fails, the sink becomes poisoned and rejects every later write in this process.
func (s *JSONL) Write(ctx context.Context, ex observation.Execution) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("write execution: %w", err)
	}
	raw, err := json.Marshal(ex)
	if err != nil {
		return fmt.Errorf("marshal execution: %w", err)
	}
	line := append(raw, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("write execution: %w", err)
	}
	if s.poisoned != nil {
		return fmt.Errorf("sink is poisoned after an unrecoverable append: %w", s.poisoned)
	}
	info, err := s.f.Stat()
	if err != nil {
		return fmt.Errorf("stat sink %s: %w", s.path, err)
	}
	start := info.Size()
	n, writeErr := s.f.Write(line)
	if writeErr == nil && n == len(line) {
		return nil
	}
	if writeErr == nil {
		writeErr = io.ErrShortWrite
	}
	if rollbackErr := s.f.Truncate(start); rollbackErr != nil {
		s.poisoned = fmt.Errorf("write execution: %w; rollback to offset %d: %v", writeErr, start, rollbackErr)
		return fmt.Errorf("sink is poisoned after an unrecoverable append: %w", s.poisoned)
	}
	return fmt.Errorf("write execution: %w (partial append rolled back to offset %d)", writeErr, start)
}

// Close closes the underlying file. Write has no userspace buffer to flush.
func (s *JSONL) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.f.Close(); err != nil {
		return fmt.Errorf("close sink %s: %w", s.path, err)
	}
	return nil
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
