package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
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

type immediateHTTPShutdown struct{}

func (immediateHTTPShutdown) Shutdown(context.Context) error { return nil }

type failingHTTPShutdown struct{ err error }

func (s failingHTTPShutdown) Shutdown(context.Context) error { return s.err }

type immediateGRPCShutdown struct{}

func (immediateGRPCShutdown) GracefulStop() {}
func (immediateGRPCShutdown) Stop()         {}

type immediateFlush struct{}

func (immediateFlush) Flush(context.Context) error { return nil }

type failingFlush struct{ err error }

func (f failingFlush) Flush(context.Context) error { return f.err }

func TestShutdownFailureExitSemantics(t *testing.T) {
	httpErr := errors.New("HTTP drain failed")
	flushErr := errors.New("persistence failed")

	tests := []struct {
		name   string
		result shutdownResult
		want   int
	}{
		{name: "normal shutdown", result: shutdownResult{}, want: 0},
		{name: "HTTP drain failure", result: shutdownResult{httpErr: httpErr}, want: 1},
		{name: "persistence failure", result: shutdownResult{flushErr: flushErr}, want: 1},
		{name: "persistence timeout", result: shutdownResult{flushErr: errFlushDeadline, flushTimedOut: true}, want: 1},
		{name: "forced gRPC stop", result: shutdownResult{grpcForced: true}, want: 1},
		{name: "sink close failure", result: shutdownResult{closeErr: errors.New("close failed")}, want: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.result.exitCode(); got != tc.want {
				t.Fatalf("exit code = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestFinalizeShutdownReportsCloseFailure(t *testing.T) {
	want := errors.New("close failed")
	result := finalizeShutdown(shutdownResult{}, func() error { return want })
	if !errors.Is(result.closeErr, want) {
		t.Fatalf("close error = %v, want %v", result.closeErr, want)
	}
	if got := result.exitCode(); got == 0 {
		t.Fatal("close failure returned a successful exit code")
	}
}

func TestFinalizeShutdownDoesNotRaceCloseAfterFlushTimeout(t *testing.T) {
	closed := false
	result := finalizeShutdown(shutdownResult{flushErr: errFlushDeadline, flushTimedOut: true}, func() error {
		closed = true
		return nil
	})
	if closed {
		t.Fatal("sink was closed while a timed-out flush may still be writing")
	}
	if got := result.exitCode(); got == 0 {
		t.Fatal("persistence-not-confirmed timeout returned a successful exit code")
	}
}

func TestShutdownServicesPropagatesHTTPAndFlushFailures(t *testing.T) {
	httpErr := errors.New("HTTP drain failed")
	flushErr := errors.New("persistence failed")
	result := shutdownServices(context.Background(), failingHTTPShutdown{httpErr}, immediateGRPCShutdown{}, failingFlush{flushErr})
	if !errors.Is(result.httpErr, httpErr) {
		t.Fatalf("HTTP error = %v, want %v", result.httpErr, httpErr)
	}
	if !errors.Is(result.flushErr, flushErr) {
		t.Fatalf("flush error = %v, want %v", result.flushErr, flushErr)
	}
	if got := result.exitCode(); got == 0 {
		t.Fatal("shutdown failures returned a successful exit code")
	}
}

type blockedGracefulStop struct {
	started  chan struct{}
	stopped  chan struct{}
	stopOnce sync.Once
}

func (s *blockedGracefulStop) GracefulStop() {
	close(s.started)
	<-s.stopped
}

func (s *blockedGracefulStop) Stop() {
	s.stopOnce.Do(func() { close(s.stopped) })
}

func TestShutdownServicesForcesStuckGRPCStopAtDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	grpcServer := &blockedGracefulStop{started: make(chan struct{}), stopped: make(chan struct{})}
	done := make(chan shutdownResult, 1)
	go func() {
		done <- shutdownServices(ctx, immediateHTTPShutdown{}, grpcServer, immediateFlush{})
	}()

	<-grpcServer.started
	cancel()

	select {
	case result := <-done:
		if !result.grpcForced {
			t.Fatal("shutdown did not report a forced gRPC stop")
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown blocked after its context was canceled")
	}
	select {
	case <-grpcServer.stopped:
	default:
		t.Fatal("gRPC Stop was not called after the shutdown deadline")
	}
}

type blockedFlush struct {
	started  chan struct{}
	release  chan struct{}
	returned chan struct{}
}

func (f *blockedFlush) Flush(context.Context) error {
	close(f.started)
	<-f.release
	close(f.returned)
	return nil
}

func TestShutdownServicesDoesNotWaitPastDeadlineForNonCooperativeFlush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	flusher := &blockedFlush{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		returned: make(chan struct{}),
	}
	done := make(chan shutdownResult, 1)
	go func() {
		done <- shutdownServices(ctx, immediateHTTPShutdown{}, immediateGRPCShutdown{}, flusher)
	}()

	<-flusher.started
	cancel()

	select {
	case result := <-done:
		if !errors.Is(result.flushErr, errFlushDeadline) {
			t.Fatalf("flush error = %v, want flush deadline error", result.flushErr)
		}
		if !errors.Is(result.flushErr, context.Canceled) {
			t.Fatalf("flush error = %v, want context cancellation cause", result.flushErr)
		}
		if !result.flushTimedOut {
			t.Fatal("shutdown did not report a timed-out flush")
		}
	case <-time.After(time.Second):
		t.Fatal("non-cooperative flush blocked shutdown after its context was canceled")
	}

	close(flusher.release)
	select {
	case <-flusher.returned:
	case <-time.After(time.Second):
		t.Fatal("released flush goroutine did not exit")
	}
}

type cooperativeFlush struct {
	started chan struct{}
	release chan struct{}
}

func (f *cooperativeFlush) Flush(context.Context) error {
	close(f.started)
	<-f.release
	return nil
}

func TestFlushProcessorWaitsForNormalFlushCompletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	flusher := &cooperativeFlush{started: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- flushProcessor(ctx, flusher) }()

	<-flusher.started
	select {
	case err := <-done:
		t.Fatalf("flush returned before persistence completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	close(flusher.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("flush error = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("completed flush did not return")
	}
}
