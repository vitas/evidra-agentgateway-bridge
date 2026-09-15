package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRunVersionExitsBeforeStartingBridge(t *testing.T) {
	observationsPath := filepath.Join(t.TempDir(), "observations.jsonl")
	t.Setenv("EVIDRA_BRIDGE_OBSERVATIONS", observationsPath)
	t.Setenv("EVIDRA_BRIDGE_LISTEN_ADDR", "not-a-listen-address")
	t.Setenv("EVIDRA_BRIDGE_GRPC_LISTEN_ADDR", "also-not-a-listen-address")

	var stdout bytes.Buffer
	if exitCode := run([]string{"--version"}, &stdout); exitCode != 0 {
		t.Fatalf("run --version exit code = %d, want 0", exitCode)
	}
	if got, want := stdout.String(), "evidra-agentgateway dev\n"; got != want {
		t.Fatalf("run --version output = %q, want %q", got, want)
	}
	if _, err := os.Stat(observationsPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("--version created observations file: stat error = %v", err)
	}
}
