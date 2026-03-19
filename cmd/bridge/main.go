package main

import (
	"log"
	"net/http"

	"github.com/vitas/evidra-agentgateway-bridge/internal/config"
)

func main() {
	cfg := config.LoadConfig()

	server := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: http.NewServeMux(),
	}

	log.Fatal(server.ListenAndServe())
}
