package config

import "os"

type Config struct {
	ListenAddr    string
	EvidraBaseURL string
	EvidraAPIKey  string
	TenantHeader  string
}

func LoadConfig() Config {
	return Config{
		ListenAddr:    envOrDefault("EVIDRA_BRIDGE_LISTEN_ADDR", ":4318"),
		EvidraBaseURL: os.Getenv("EVIDRA_BASE_URL"),
		EvidraAPIKey:  os.Getenv("EVIDRA_API_KEY"),
		TenantHeader:  os.Getenv("EVIDRA_TENANT_HEADER"),
	}
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
