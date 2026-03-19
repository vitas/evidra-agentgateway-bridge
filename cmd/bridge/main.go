package main

import (
	"log"
	"net/http"

	"github.com/vitas/evidra-agentgateway-bridge/internal/bridge"
	"github.com/vitas/evidra-agentgateway-bridge/internal/config"
	"github.com/vitas/evidra-agentgateway-bridge/internal/evidra"
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

	log.Fatal(server.ListenAndServe())
}
