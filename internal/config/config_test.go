package config

import (
	"os"
	"testing"
)

func TestLoad_RPCEndpointOptional(t *testing.T) {
	os.Unsetenv("RPC_ENDPOINT")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.RPCEndpoint != "" {
		t.Errorf("expected empty RPCEndpoint, got '%s'", cfg.RPCEndpoint)
	}
}

func TestLoad_Defaults(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Network != "public" {
		t.Errorf("expected network 'public', got '%s'", cfg.Network)
	}
	if cfg.BatchSize != 100 {
		t.Errorf("expected batch size 100, got %d", cfg.BatchSize)
	}
	if cfg.WorkerCount != 8 {
		t.Errorf("expected worker count 8, got %d", cfg.WorkerCount)
	}
	if cfg.MetricsAddr != "" {
		t.Errorf("expected metrics server disabled by default, got MetricsAddr=%q", cfg.MetricsAddr)
	}
}

func TestLoad_MetricsAddrOptIn(t *testing.T) {
	os.Setenv("METRICS_ADDR", ":9090")
	defer os.Unsetenv("METRICS_ADDR")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.MetricsAddr != ":9090" {
		t.Errorf("expected MetricsAddr ':9090', got '%s'", cfg.MetricsAddr)
	}
}

func TestLoad_EnvOverride(t *testing.T) {
	os.Setenv("NETWORK", "testnet")
	os.Setenv("BATCH_SIZE", "200")
	defer func() {
		os.Unsetenv("NETWORK")
		os.Unsetenv("BATCH_SIZE")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Network != "testnet" {
		t.Errorf("expected network 'testnet', got '%s'", cfg.Network)
	}
	if cfg.BatchSize != 200 {
		t.Errorf("expected batch size 200, got %d", cfg.BatchSize)
	}
}

func TestLoad_InvalidNetwork(t *testing.T) {
	os.Setenv("NETWORK", "mainnet")
	defer os.Unsetenv("NETWORK")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid NETWORK, got nil")
	}
}

func TestLoad_ZeroWorkerCount(t *testing.T) {
	os.Setenv("WORKER_COUNT", "0")
	defer os.Unsetenv("WORKER_COUNT")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for WORKER_COUNT=0, got nil")
	}
}

func TestLoad_NegativeWorkerCount(t *testing.T) {
	os.Setenv("WORKER_COUNT", "-1")
	defer os.Unsetenv("WORKER_COUNT")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for WORKER_COUNT=-1, got nil")
	}
}

func TestLoad_ZeroBatchSize(t *testing.T) {
	os.Setenv("BATCH_SIZE", "0")
	defer os.Unsetenv("BATCH_SIZE")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for BATCH_SIZE=0, got nil")
	}
}

func TestLoad_NegativeBatchSize(t *testing.T) {
	os.Setenv("BATCH_SIZE", "-5")
	defer os.Unsetenv("BATCH_SIZE")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for BATCH_SIZE=-5, got nil")
	}
}

func TestLoad_APIAddrDefault(t *testing.T) {
	os.Unsetenv("API_ADDR")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.APIAddr != ":8080" {
		t.Errorf("expected default APIAddr ':8080', got '%s'", cfg.APIAddr)
	}
}

func TestLoad_APIAddrOverride(t *testing.T) {
	os.Setenv("API_ADDR", "127.0.0.1:9000")
	defer os.Unsetenv("API_ADDR")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.APIAddr != "127.0.0.1:9000" {
		t.Errorf("expected APIAddr '127.0.0.1:9000', got '%s'", cfg.APIAddr)
	}
}

func TestLoad_InvalidAPIAddr(t *testing.T) {
	os.Setenv("API_ADDR", "8080")
	defer os.Unsetenv("API_ADDR")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for API_ADDR without a port separator, got nil")
	}
}

func TestLoad_CORSOriginsDefaultToWildcard(t *testing.T) {
	os.Unsetenv("API_CORS_ORIGINS")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.APICORSOrigins) != 1 || cfg.APICORSOrigins[0] != "*" {
		t.Errorf("expected default APICORSOrigins ['*'], got %v", cfg.APICORSOrigins)
	}
}

func TestLoad_CORSOriginsSplitAndTrimmed(t *testing.T) {
	os.Setenv("API_CORS_ORIGINS", " https://a.example.com , https://b.example.com ,")
	defer os.Unsetenv("API_CORS_ORIGINS")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"https://a.example.com", "https://b.example.com"}
	if len(cfg.APICORSOrigins) != len(want) {
		t.Fatalf("got %v, want %v", cfg.APICORSOrigins, want)
	}
	for i := range want {
		if cfg.APICORSOrigins[i] != want[i] {
			t.Errorf("origin %d = %q, want %q", i, cfg.APICORSOrigins[i], want[i])
		}
	}
}
