package main

import (
	"log"
	"net/http"

	"github.com/vitas/evidra-agentgateway-bridge/internal/config"
	"github.com/vitas/evidra-agentgateway-bridge/internal/otlphttp"
)

func main() {
	cfg := config.LoadConfig()

	mux := http.NewServeMux()
	mux.Handle("/v1/logs", otlphttp.NewLogsHandler(nil))
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
