package config

import "testing"

func TestLoadConfig_DefaultListenAddr(t *testing.T) {
	t.Setenv("EVIDRA_BRIDGE_LISTEN_ADDR", "")

	cfg := LoadConfig()
	if cfg.ListenAddr != ":4318" {
		t.Fatalf("ListenAddr=%q, want %q", cfg.ListenAddr, ":4318")
	}
}

func TestLoadConfig_GRPCDefaults(t *testing.T) {
	t.Setenv("EVIDRA_BRIDGE_GRPC_LISTEN_ADDR", "")

	cfg := LoadConfig()
	if cfg.GRPCListenAddr != ":4317" {
		t.Fatalf("GRPCListenAddr=%q, want %q", cfg.GRPCListenAddr, ":4317")
	}
}

func TestLoadConfig_GRPCOverride(t *testing.T) {
	t.Setenv("EVIDRA_BRIDGE_GRPC_LISTEN_ADDR", ":9317")

	cfg := LoadConfig()
	if cfg.GRPCListenAddr != ":9317" {
		t.Fatalf("GRPCListenAddr=%q, want %q", cfg.GRPCListenAddr, ":9317")
	}
}
