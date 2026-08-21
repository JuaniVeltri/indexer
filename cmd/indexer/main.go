package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/miguelnietoa/stellar-explorer/indexer/internal/config"
	"github.com/miguelnietoa/stellar-explorer/indexer/internal/httpserver"
	"github.com/miguelnietoa/stellar-explorer/indexer/internal/pipeline"
	"github.com/miguelnietoa/stellar-explorer/indexer/internal/publisher"
	"github.com/miguelnietoa/stellar-explorer/indexer/internal/source"
	"github.com/miguelnietoa/stellar-explorer/indexer/internal/store"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	fmt.Printf("StellarView Indexer\n")
	fmt.Printf("  Network:    %s\n", cfg.Network)
	fmt.Printf("  RPC:        %s\n", cfg.RPCEndpoint)
	fmt.Printf("  Database:   %s\n", cfg.DatabaseURL)
	fmt.Printf("  Workers:    %d\n", cfg.WorkerCount)

	if len(os.Args) < 2 {
		fmt.Println("Usage: indexer <live|backfill|s3backfill|serve|analytics-backfill|migrate>")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "live":
		if cfg.RPCEndpoint == "" {
			log.Fatal("RPC_ENDPOINT is required for live command")
		}
		runLive(cfg)
	case "backfill":
		if cfg.RPCEndpoint == "" {
			log.Fatal("RPC_ENDPOINT is required for backfill command")
		}
		runBackfill(cfg)
	case "s3backfill":
		runS3Backfill(cfg)
	case "serve":
		runServe(cfg)
	case "analytics-backfill":
		runAnalyticsBackfill(cfg)
	case "migrate":
		runMigrate(cfg.DatabaseURL)
	default:
		log.Fatalf("Unknown command: %s. Use: live, backfill, s3backfill, serve, analytics-backfill, migrate", os.Args[1])
	}
}

func setupContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, shutting down...", sig)
		cancel()
	}()
	return ctx, cancel
}

func initDeps(cfg *config.Config, passphrase string) (*store.PostgresStore, *source.RPCClient) {
	db, err := store.NewPostgresStore(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	rpc := source.NewRPCClient(cfg.RPCEndpoint, passphrase)
	return db, rpc
}

func runLive(cfg *config.Config) {
	ctx, cancel := setupContext()
	defer cancel()

	passphrase, err := cfg.NetworkPassphrase()
	if err != nil {
		log.Fatalf("Failed to resolve network passphrase: %v", err)
	}

	db, rpc := initDeps(cfg, passphrase)
	defer db.Close()

	if cfg.MetricsAddr != "" {
		srv := httpserver.New(cfg.MetricsAddr, db.DB(), db)
		go func() {
			log.Printf("metrics server listening on %s (/metrics, /healthz)", cfg.MetricsAddr)
			if err := srv.Start(); err != nil {
				log.Printf("metrics server error: %v", err)
			}
		}()
		defer func() {
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutdownCancel()
			if err := srv.Shutdown(shutdownCtx); err != nil {
				log.Printf("metrics server shutdown error: %v", err)
			}
		}()
	}

	p := pipeline.NewLivePipeline(rpc, db, passphrase, cfg.BatchSize)

	// Attach Redis publisher if configured
	if cfg.RedisURL != "" {
		pub, err := publisher.NewRedisPublisher(cfg.RedisURL)
		if err != nil {
			log.Printf("Warning: Redis publisher unavailable: %v", err)
		} else {
			defer pub.Close()
			p.SetPublisher(pub)
			log.Println("Redis publisher attached")
		}
	}

	log.Println("Starting live ingestion...")
	if err := p.Run(ctx); err != nil && err != context.Canceled {
		log.Fatalf("Live pipeline failed: %v", err)
	}
	log.Println("Shutdown complete.")
}

// runServe starts the analytics read API without ingesting anything. This is
// the process the explorer points NEXT_PUBLIC_INDEXER_URL at; the same routes
// are also mounted on the live command's metrics server for local development.
func runServe(cfg *config.Config) {
	ctx, cancel := setupContext()
	defer cancel()

	db, err := store.NewPostgresStore(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()

	srv := httpserver.New(cfg.APIAddr, db.DB(), db)

	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("api server shutdown error: %v", err)
		}
	}()

	log.Printf("analytics API listening on %s (/api/v1/analytics, /metrics, /healthz)", cfg.APIAddr)
	if err := srv.Start(); err != nil {
		log.Fatalf("API server failed: %v", err)
	}
	log.Println("Shutdown complete.")
}

func runBackfill(cfg *config.Config) {
	startLedger, endLedger := parseBackfillFlags()

	ctx, cancel := setupContext()
	defer cancel()

	passphrase, err := cfg.NetworkPassphrase()
	if err != nil {
		log.Fatalf("Failed to resolve network passphrase: %v", err)
	}

	db, rpc := initDeps(cfg, passphrase)
	defer db.Close()

	p := pipeline.NewBackfillPipeline(rpc, db, passphrase, cfg.BatchSize, cfg.WorkerCount)

	log.Printf("Starting backfill from ledger %d to %d...", startLedger, endLedger)
	if err := p.Run(ctx, startLedger, endLedger); err != nil && err != context.Canceled {
		log.Fatalf("Backfill failed: %v", err)
	}
	log.Println("Backfill complete.")
}

func runS3Backfill(cfg *config.Config) {
	startLedger, endLedger := parseBackfillFlags()

	ctx, cancel := setupContext()
	defer cancel()

	db, err := store.NewPostgresStore(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()

	p := pipeline.NewS3BackfillPipeline(db, cfg.WorkerCount)

	log.Printf("Starting S3 data lake backfill from ledger %d to %d...", startLedger, endLedger)
	if err := p.Run(ctx, startLedger, endLedger); err != nil && err != context.Canceled {
		log.Fatalf("S3 backfill failed: %v", err)
	}
	log.Println("S3 backfill complete.")
}

// runAnalyticsBackfill populates the analytics continuous aggregates from data
// already in the database. The migration creates them empty so it stays instant
// on a populated database; this is the one-off that fills them in.
//
// Re-running is safe and cheap: TimescaleDB commits each batch separately and
// skips buckets that are already materialized, so an interrupted run resumes.
func runAnalyticsBackfill(cfg *config.Config) {
	from, to := parseAnalyticsWindowFlags()

	ctx, cancel := setupContext()
	defer cancel()

	db, err := store.NewPostgresStore(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()

	log.Println("Refreshing analytics aggregates...")
	results, err := db.RefreshAnalyticsAggregates(ctx, from, to)

	// Report whatever completed before reacting to a failure, so a partial run
	// still tells the operator where it got to.
	for _, r := range results {
		if r.Skipped {
			log.Printf("  %-38s skipped: window holds no complete bucket", r.Aggregate)
			continue
		}
		log.Printf("  %-38s refreshed in %s", r.Aggregate, r.Duration.Round(time.Millisecond))
	}

	if err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("Analytics backfill failed: %v", err)
	}
	log.Println("Analytics backfill complete.")
}

// parseAnalyticsWindowFlags reads the optional --from/--to RFC 3339 bounds.
// An omitted bound means "as far as the data goes" in that direction.
func parseAnalyticsWindowFlags() (from, to time.Time) {
	parse := func(flag, raw string) time.Time {
		ts, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			log.Fatalf("Invalid %s value %q: expected RFC 3339, e.g. 2026-01-01T00:00:00Z", flag, raw)
		}
		return ts.UTC()
	}

	for i := 2; i < len(os.Args)-1; i++ {
		switch os.Args[i] {
		case "--from":
			from = parse("--from", os.Args[i+1])
		case "--to":
			to = parse("--to", os.Args[i+1])
		}
	}

	if !from.IsZero() && !to.IsZero() && !from.Before(to) {
		log.Fatalf("Invalid window: --from (%s) must be before --to (%s)",
			from.Format(time.RFC3339), to.Format(time.RFC3339))
	}
	return from, to
}

func parseBackfillFlags() (uint32, uint32) {
	var startLedger, endLedger uint32

	for i := 2; i < len(os.Args)-1; i++ {
		switch os.Args[i] {
		case "--start":
			n, err := strconv.ParseUint(os.Args[i+1], 10, 32)
			if err != nil {
				log.Fatalf("Invalid --start value: %v", err)
			}
			startLedger = uint32(n)
		case "--end":
			n, err := strconv.ParseUint(os.Args[i+1], 10, 32)
			if err != nil {
				log.Fatalf("Invalid --end value: %v", err)
			}
			endLedger = uint32(n)
		}
	}

	if startLedger == 0 || endLedger == 0 {
		log.Fatal("Usage: indexer backfill --start <ledger> --end <ledger>")
	}

	return startLedger, endLedger
}
