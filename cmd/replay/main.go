package main

import (
	"bytes"
	"fmt"
	"net/http"
	"os"

	"github.com/vitas/evidra-agentgateway-bridge/internal/replay"
)

func main() {
	payload, err := replay.BuildFixturePayload([]string{
		"testdata/agentgateway/log_record_minimal.json",
		"testdata/agentgateway/log_record_outcome.json",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "build fixture payload: %v\n", err)
		os.Exit(1)
	}

	bridgeURL := os.Getenv("EVIDRA_BRIDGE_URL")
	if bridgeURL == "" {
		bridgeURL = "http://localhost:4318"
	}

	req, err := http.NewRequest(http.MethodPost, bridgeURL+"/v1/logs", bytes.NewReader(payload))
	if err != nil {
		fmt.Fprintf(os.Stderr, "build request: %v\n", err)
		os.Exit(1)
	}
	req.Header.Set("Content-Type", "application/x-protobuf")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "send request: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusAccepted {
		fmt.Fprintf(os.Stderr, "unexpected status: %s\n", resp.Status)
		os.Exit(1)
	}
}
