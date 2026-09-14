package config

import (
	"os"
	"time"
)

// Config is the receiver's environment.
//
// The old EVIDRA_BASE_URL / EVIDRA_API_KEY pair is gone rather than deprecated: they pointed
// at the hosted ingest API that §43 deleted from the product, and a knob for a nonexistent
// endpoint is worse than no knob. "Accept-only mode" made a receiver that forwarded nothing
// look like one that was merely unconfigured, which is the failure shape this bridge exists to
// stop existing elsewhere.
type Config struct {
	// ListenAddr serves OTLP/HTTP: /v1/logs and /v1/traces.
	ListenAddr string
	// GRPCListenAddr serves OTLP/gRPC, which is what AgentGateway's tracing and access-log
	// export default to.
	GRPCListenAddr string
	// ObservationsPath is the JSONL artifact every normalized execution is appended to.
	ObservationsPath string
	// MergeWait bounds how long a record seen on only one signal waits for the other before it
	// is emitted as partial. Zero holds partials until the process is told to flush.
	MergeWait time.Duration
	// ObserverID and ObserverVersion are written onto every execution's source, so records
	// produced by two receivers can be told apart after the fact.
	ObserverID      string
	ObserverVersion string
}

func LoadConfig() Config {
	return Config{
		ListenAddr:       envOrDefault("EVIDRA_BRIDGE_LISTEN_ADDR", ":4318"),
		GRPCListenAddr:   envOrDefault("EVIDRA_BRIDGE_GRPC_LISTEN_ADDR", ":4317"),
		ObservationsPath: envOrDefault("EVIDRA_BRIDGE_OBSERVATIONS", "output/observations.jsonl"),
		MergeWait:        envDurationOrDefault("EVIDRA_BRIDGE_MERGE_WAIT", 30*time.Second),
		ObserverID:       envOrDefault("EVIDRA_BRIDGE_OBSERVER_ID", "agentgateway-bridge"),
		ObserverVersion:  os.Getenv("EVIDRA_BRIDGE_OBSERVER_VERSION"),
	}
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envDurationOrDefault(key string, fallback time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		// A malformed wait falls back rather than becoming zero, because zero would mean
		// "never emit a partial record" - a silent change in behaviour caused by a typo.
		return fallback
	}
	return parsed
}
