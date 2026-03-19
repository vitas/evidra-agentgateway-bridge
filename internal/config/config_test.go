package config

import "testing"

func TestLoadConfig_DefaultListenAddr(t *testing.T) {
	t.Setenv("EVIDRA_BRIDGE_LISTEN_ADDR", "")

	cfg := LoadConfig()
	if cfg.ListenAddr != ":4318" {
		t.Fatalf("ListenAddr=%q, want %q", cfg.ListenAddr, ":4318")
	}
}
