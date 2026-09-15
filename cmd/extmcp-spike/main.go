package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/vitas/evidra-agentgateway-bridge/internal/extmcp"
	api "github.com/vitas/evidra-agentgateway-bridge/internal/extmcp/api"
	"google.golang.org/grpc"
)

func main() {
	addr := os.Getenv("EVIDRA_EXTMCP_LISTEN_ADDR")
	if addr == "" {
		addr = ":19090"
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen: %v\n", err)
		os.Exit(1)
	}
	server := grpc.NewServer()
	store := extmcp.NewStore()
	api.RegisterExtMcpServer(server, extmcp.NewServer(store))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		server.GracefulStop()
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"captures":  store.Snapshot(),
			"hook_gaps": []string{"AgentGateway cannot invoke CheckResponse for a response that never arrives; missing-response is observed as start-only."},
		})
	}()
	if err := server.Serve(listener); err != nil && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		os.Exit(1)
	}
}
