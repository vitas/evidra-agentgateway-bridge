package main

import (
	"context"
	"encoding/json"
	"errors"
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
)

func main() {
	cfg := config.LoadConfig()

	out, err := sink.NewJSONL(cfg.ObservationsPath)
	if err != nil {
		log.Fatalf("observations sink: %v", err)
	}
	defer out.Close()

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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
	grpcSrv.GracefulStop()

	// Flush before exiting. Records seen on only one signal are still pending, and a receiver
	// that drops them on shutdown reports a coverage gap that was actually a lifecycle bug.
	if err := processor.Flush(context.Background()); err != nil {
		log.Printf("flush: %v", err)
	}
	stats := processor.Stats()
	raw, _ := json.Marshal(stats)
	log.Printf("final stats: %s", raw)
}
