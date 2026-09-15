package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	// Install signal handling before the listener becomes externally ready. A supervisor may
	// send SIGTERM immediately after its first successful readiness probe.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen: %v\n", err)
		os.Exit(1)
	}
	if err := serveExtMCP(ctx, listener, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		os.Exit(1)
	}
}

// serveExtMCP owns shutdown sequencing. Serve must return before capture serialization so main
// cannot exit while a signal goroutine is still writing the decision artifact.
func serveExtMCP(ctx context.Context, listener net.Listener, stdout io.Writer) error {
	server := grpc.NewServer()
	store := extmcp.NewStore()
	api.RegisterExtMcpServer(server, extmcp.NewServer(store))

	stopped := make(chan struct{})
	go func() {
		<-ctx.Done()
		server.GracefulStop()
		close(stopped)
	}()
	serveErr := server.Serve(listener)
	if ctx.Err() == nil {
		return serveErr
	}
	<-stopped
	if serveErr != nil && !errors.Is(serveErr, grpc.ErrServerStopped) {
		return serveErr
	}
	return json.NewEncoder(stdout).Encode(map[string]any{
		"captures":  store.Snapshot(),
		"hook_gaps": []string{"AgentGateway cannot invoke CheckResponse for a response that never arrives; missing-response is observed as start-only."},
	})
}
