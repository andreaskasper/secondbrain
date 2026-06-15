package main

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds the runtime configuration, sourced from environment variables.
type Config struct {
	// APIKey is the shared secret required for every request (except /healthz).
	APIKey string
	// DataDir is the root directory on disk where the markdown tree lives.
	DataDir string
	// Addr is the listen address, e.g. ":8080".
	Addr string
	// MaxBodyBytes caps the size of request bodies for write operations.
	MaxBodyBytes int64
}

// LoadConfig reads configuration from the environment and validates it.
func LoadConfig() (Config, error) {
	cfg := Config{
		APIKey:       os.Getenv("SECONDBRAIN_API_KEY"),
		DataDir:      envOr("SECONDBRAIN_DATA_DIR", "/data"),
		Addr:         envOr("SECONDBRAIN_ADDR", ":8080"),
		MaxBodyBytes: 10 << 20, // 10 MiB
	}

	if v := os.Getenv("SECONDBRAIN_MAX_BODY_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("invalid SECONDBRAIN_MAX_BODY_BYTES: %q", v)
		}
		cfg.MaxBodyBytes = n
	}

	if cfg.APIKey == "" {
		return Config{}, fmt.Errorf("SECONDBRAIN_API_KEY must be set (refusing to start without authentication)")
	}

	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
