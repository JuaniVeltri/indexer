package config

import (
	"fmt"
	"net"
	"os"
	"strconv"

	"github.com/stellar/go-stellar-sdk/network"
)

type Config struct {
	DatabaseURL  string
	RedisURL     string
	RPCEndpoint  string
	DataLakePath string
	Network      string // "public", "testnet", "futurenet"
	BatchSize    int
	WorkerCount  int
	MetricsAddr  string // listen address for /metrics and /healthz; disabled when empty
	APIAddr      string // listen address for the read API served by the serve command
}

func Load() (*Config, error) {
	cfg := &Config{
		DatabaseURL:  getEnv("DATABASE_URL", "postgresql://explorer:explorer_dev@localhost:54320/stellar_explorer?sslmode=disable"),
		RedisURL:     getEnv("REDIS_URL", "redis://localhost:63790"),
		RPCEndpoint:  getEnv("RPC_ENDPOINT", ""),
		DataLakePath: getEnv("DATA_LAKE_PATH", "s3://aws-public-blockchain/v1.1/stellar/ledgers/pubnet"),
		Network:      getEnv("NETWORK", "public"),
		BatchSize:    getEnvInt("BATCH_SIZE", 100),
		WorkerCount:  getEnvInt("WORKER_COUNT", 8),
		MetricsAddr:  getEnv("METRICS_ADDR", ""),
		APIAddr:      getEnv("API_ADDR", ":8080"),
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) validate() error {
	switch c.Network {
	case "public", "testnet", "futurenet":
		// valid
	default:
		return fmt.Errorf("invalid NETWORK %q: must be one of public, testnet, futurenet", c.Network)
	}

	if c.WorkerCount <= 0 {
		return fmt.Errorf("invalid WORKER_COUNT %d: must be > 0", c.WorkerCount)
	}

	if c.BatchSize <= 0 {
		return fmt.Errorf("invalid BATCH_SIZE %d: must be > 0", c.BatchSize)
	}

	// Caught here rather than at ListenAndServe, so a typo fails at startup
	// instead of after the process has already reported itself as running.
	if _, _, err := net.SplitHostPort(c.APIAddr); err != nil {
		return fmt.Errorf("invalid API_ADDR %q: must be a host:port listen address", c.APIAddr)
	}

	return nil
}

// NetworkPassphrase returns the Stellar network passphrase for the configured network.
func (c *Config) NetworkPassphrase() (string, error) {
	switch c.Network {
	case "public":
		return network.PublicNetworkPassphrase, nil
	case "testnet":
		return network.TestNetworkPassphrase, nil
	case "futurenet":
		return network.FutureNetworkPassphrase, nil
	default:
		return "", fmt.Errorf("unknown network: %s", c.Network)
	}
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if val := os.Getenv(key); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			return n
		}
	}
	return fallback
}
