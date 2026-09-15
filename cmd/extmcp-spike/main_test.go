package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestServeExtMCPAlwaysSerializesCaptureAfterShutdown(t *testing.T) {
	for iteration := 0; iteration < 50; iteration++ {
		ctx, cancel := context.WithCancel(context.Background())
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}

		var stdout bytes.Buffer
		done := make(chan error, 1)
		go func() { done <- serveExtMCP(ctx, listener, &stdout) }()
		cancel()

		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("iteration %d: serve error: %v", iteration, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("iteration %d: shutdown did not complete", iteration)
		}

		var capture struct {
			Captures []any    `json:"captures"`
			HookGaps []string `json:"hook_gaps"`
		}
		if err := json.NewDecoder(&stdout).Decode(&capture); err != nil {
			t.Fatalf("iteration %d: capture output %q is not JSON: %v", iteration, stdout.String(), err)
		}
		if capture.Captures == nil || len(capture.HookGaps) != 1 {
			t.Fatalf("iteration %d: incomplete capture output: %+v", iteration, capture)
		}
	}
}

func TestExtMCPSpikeEmitsCaptureForEveryReadySIGTERM(t *testing.T) {
	for iteration := 0; iteration < 25; iteration++ {
		probe, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := probe.Addr().String()
		if err := probe.Close(); err != nil {
			t.Fatal(err)
		}

		var stdout, stderr bytes.Buffer
		cmd := exec.Command(os.Args[0], "-test.run=^TestExtMCPSpikeSignalHelperProcess$")
		cmd.Env = append(os.Environ(), "GO_WANT_EXTMCP_SIGNAL_HELPER=1", "EVIDRA_EXTMCP_LISTEN_ADDR="+addr)
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		ready := false
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			conn, dialErr := net.DialTimeout("tcp", addr, 20*time.Millisecond)
			if dialErr == nil {
				_ = conn.Close()
				ready = true
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if !ready {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatalf("iteration %d: helper did not become ready: %s", iteration, stderr.String())
		}
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatalf("iteration %d: signal helper: %v", iteration, err)
		}
		if err := cmd.Wait(); err != nil {
			t.Fatalf("iteration %d: helper exit: %v; stderr: %s", iteration, err, stderr.String())
		}

		var capture struct {
			Captures []any    `json:"captures"`
			HookGaps []string `json:"hook_gaps"`
		}
		if err := json.NewDecoder(&stdout).Decode(&capture); err != nil {
			t.Fatalf("iteration %d: capture output %q is not JSON: %v", iteration, stdout.String(), err)
		}
		if capture.Captures == nil || len(capture.HookGaps) != 1 {
			t.Fatalf("iteration %d: incomplete capture output: %+v", iteration, capture)
		}
	}
}

func TestExtMCPSpikeSignalHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_EXTMCP_SIGNAL_HELPER") != "1" {
		return
	}
	main()
}
