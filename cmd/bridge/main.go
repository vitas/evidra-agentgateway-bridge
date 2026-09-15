package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/vitas/evidra-agentgateway-bridge/internal/bridge"
	"github.com/vitas/evidra-agentgateway-bridge/internal/config"
	"github.com/vitas/evidra-agentgateway-bridge/internal/otlpgrpc"
	"github.com/vitas/evidra-agentgateway-bridge/internal/otlphttp"
	"github.com/vitas/evidra-agentgateway-bridge/internal/sink"
	"github.com/vitas/evidra-agentgateway-bridge/internal/version"
)

const shutdownTimeout = 10 * time.Second

type httpShutdowner interface {
	Shutdown(context.Context) error
}

type grpcShutdowner interface {
	GracefulStop()
	Stop()
}

// processorFlusher follows the usual context contract: it must stop work and return when ctx
// is canceled. Keeping Flush synchronous guarantees no writer survives run's return.
type processorFlusher interface {
	Flush(context.Context) error
}

type shutdownResult struct {
	httpErr    error
	flushErr   error
	grpcForced bool
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}

func run(args []string, stdout io.Writer) int {
	if len(args) == 1 && args[0] == "--version" {
		_, _ = fmt.Fprintf(stdout, "evidra-agentgateway %s\n", version.Version)
		return 0
	}

	cfg := config.LoadConfig()

	out, err := sink.NewJSONL(cfg.ObservationsPath)
	if err != nil {
		log.Fatalf("observations sink: %v", err)
	}
	defer func() {
		if err := out.Close(); err != nil {
			log.Printf("close observations sink: %v", err)
		}
	}()

	processor := bridge.NewProcessor(out, cfg.MergeWait)
	processor.SetObserver(cfg.ObserverID, cfg.ObserverVersion)
	log.Printf("normalizing OTLP logs and traces into %s (merge wait %s)", cfg.ObservationsPath, cfg.MergeWait)

	// gRPC OTLP receiver: this is what AgentGateway's tracing and access-log export use by
	// default (port 4317, /v1/logs).
	grpcSrv := otlpgrpc.NewServer(processor, processor)
	grpcLis, err := net.Listen("tcp", cfg.GRPCListenAddr)
	if err != nil {
		log.Fatalf("gRPC listen %s: %v", cfg.GRPCListenAddr, err)
	}
	go func() {
		log.Printf("gRPC OTLP receiver listening on %s", cfg.GRPCListenAddr)
		if err := grpcSrv.Serve(grpcLis); err != nil {
			log.Printf("gRPC serve stopped: %v", err)
		}
	}()

	mux := http.NewServeMux()
	mux.Handle("/v1/logs", otlphttp.NewLogsHandler(processor))
	mux.Handle("/v1/traces", otlphttp.NewTracesHandler(processor))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	// /stats is how a parity run tells "the receiver saw nothing" from "the receiver saw it and
	// could not join it". Without it those two look identical from the outside, which is how
	// the old correlation bug survived: the forwarder returned success and dropped the outcome.
	mux.HandleFunc("/stats", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(processor.Stats())
	})

	server := &http.Server{Addr: cfg.ListenAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	go func() {
		log.Printf("HTTP OTLP receiver listening on %s", cfg.ListenAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http serve: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Print("shutting down")

	// HTTP drain, gRPC drain, and the final processor flush share one budget. A busy
	// earlier phase therefore cannot silently turn each later phase into another 10-second wait.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	result := shutdownServices(shutdownCtx, server, grpcSrv, processor)
	if result.httpErr != nil {
		log.Printf("HTTP shutdown: %v", result.httpErr)
	}
	if result.grpcForced {
		log.Print("gRPC graceful shutdown exceeded the shutdown budget; forced stop")
	}
	if result.flushErr != nil {
		log.Printf("flush: %v", result.flushErr)
	}
	stats := processor.Stats()
	raw, _ := json.Marshal(stats)
	log.Printf("final stats: %s", raw)

	return 0
}

func shutdownServices(
	ctx context.Context,
	httpServer httpShutdowner,
	grpcServer grpcShutdowner,
	processor processorFlusher,
) shutdownResult {
	result := shutdownResult{httpErr: httpServer.Shutdown(ctx)}
	result.grpcForced = stopGRPC(ctx, grpcServer)
	result.flushErr = flushProcessor(ctx, processor)
	return result
}

func stopGRPC(ctx context.Context, server grpcShutdowner) bool {
	done := make(chan struct{})
	go func() {
		server.GracefulStop()
		close(done)
	}()

	select {
	case <-done:
		return false
	case <-ctx.Done():
		// grpc.Server.Stop interrupts active RPCs and makes GracefulStop return.
		server.Stop()
		<-done
		return true
	}
}

func flushProcessor(ctx context.Context, processor processorFlusher) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return processor.Flush(ctx)
}
