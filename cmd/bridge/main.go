package main

import (
	"log"
	"net"
	"net/http"

	"github.com/vitas/evidra-agentgateway-bridge/internal/bridge"
	"github.com/vitas/evidra-agentgateway-bridge/internal/config"
	"github.com/vitas/evidra-agentgateway-bridge/internal/evidra"
	"github.com/vitas/evidra-agentgateway-bridge/internal/otlpgrpc"
	"github.com/vitas/evidra-agentgateway-bridge/internal/otlphttp"
)

func main() {
	cfg := config.LoadConfig()

	var processor *bridge.Processor
	if cfg.EvidraBaseURL != "" && cfg.EvidraAPIKey != "" {
		processor = bridge.NewProcessor(evidra.NewClient(cfg.EvidraBaseURL, cfg.EvidraAPIKey, nil))
		log.Printf("forwarding OTLP logs and traces to Evidra at %s", cfg.EvidraBaseURL)
	} else {
		log.Printf("starting in accept-only mode; set EVIDRA_BASE_URL and EVIDRA_API_KEY to enable forwarding")
	}

	// gRPC OTLP receiver (for AgentGateway direct export).
	grpcSrv := otlpgrpc.NewServer(processor)
	grpcLis, err := net.Listen("tcp", cfg.GRPCListenAddr)
	if err != nil {
		log.Fatalf("gRPC listen %s: %v", cfg.GRPCListenAddr, err)
	}
	go func() {
		log.Printf("gRPC OTLP receiver listening on %s", cfg.GRPCListenAddr)
		if err := grpcSrv.Serve(grpcLis); err != nil {
			log.Fatalf("gRPC serve: %v", err)
		}
	}()

	// HTTP OTLP receiver (existing).
	mux := http.NewServeMux()
	mux.Handle("/v1/logs", otlphttp.NewLogsHandler(processor))
	mux.Handle("/v1/traces", otlphttp.NewTracesHandler(processor))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	server := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: mux,
	}

	log.Printf("HTTP OTLP receiver listening on %s", cfg.ListenAddr)
	log.Fatal(server.ListenAndServe())
}
