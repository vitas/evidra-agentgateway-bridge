package config

import "os"

type Config struct {
	ListenAddr     string
	GRPCListenAddr string
	EvidraBaseURL  string
	EvidraAPIKey   string
	TenantHeader   string
}

func LoadConfig() Config {
	return Config{
		ListenAddr:     envOrDefault("EVIDRA_BRIDGE_LISTEN_ADDR", ":4318"),
		GRPCListenAddr: envOrDefault("EVIDRA_BRIDGE_GRPC_LISTEN_ADDR", ":4317"),
		EvidraBaseURL:  os.Getenv("EVIDRA_BASE_URL"),
		EvidraAPIKey:   os.Getenv("EVIDRA_API_KEY"),
		TenantHeader:   os.Getenv("EVIDRA_TENANT_HEADER"),
	}
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
