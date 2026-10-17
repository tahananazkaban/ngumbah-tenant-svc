// Command tenant-svc serves the tenant gRPC API and manages its database.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/tahananazkaban/ngumbah-tenant-svc/internal/config"
	"github.com/tahananazkaban/ngumbah-tenant-svc/internal/server"
	"github.com/tahananazkaban/ngumbah-tenant-svc/internal/storage"
	"github.com/tahananazkaban/ngumbah-tenant-svc/internal/tenant"
)

const usage = `usage: tenant-svc [command]

commands:
  serve     run the gRPC server (default)
  migrate   apply pending database migrations
  seed      create or refresh the demo tenant
  health    probe the local server's gRPC health (for container health checks)`

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	// health must not need database settings, so it runs before config validation
	if cmd == "health" {
		os.Exit(probeHealth())
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid configuration:\n%v\n", err)
		os.Exit(2)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})).
		With("service", "tenant-svc")
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cmd {
	case "serve":
		err = serve(ctx, cfg, logger)
	case "migrate":
		err = migrate(ctx, cfg, logger)
	case "seed":
		err = seed(ctx, cfg, logger)
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		logger.Error("command failed", "command", cmd, "error", err)
		os.Exit(1)
	}
}

func serve(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	shutdownTracing := setupTracing()
	defer shutdownTracing()

	pool, err := storage.Open(ctx, storage.Config{URL: cfg.DatabaseURL})
	if err != nil {
		return err
	}
	defer pool.Close()

	if cfg.Development() {
		if err := storage.Migrate(ctx, pool); err != nil {
			return err
		}
		logger.Info("migrations applied (development)")
	}

	srv := server.New(server.Options{
		Tenants:    tenant.NewService(tenant.NewRepository(pool)),
		Pinger:     pool,
		Logger:     logger,
		Reflection: cfg.Development(),
	})

	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.GRPCAddr, err)
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(lis) }()
	logger.Info("serving", "addr", lis.Addr().String(), "env", cfg.Env)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	logger.Info("shutting down")
	done := make(chan struct{})
	go func() { srv.GracefulStop(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		srv.Stop()
	}
	return nil
}

func migrate(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	pool, err := storage.Open(ctx, storage.Config{URL: cfg.DatabaseURL})
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := storage.Migrate(ctx, pool); err != nil {
		return err
	}
	version, err := storage.MigrationVersion(ctx, pool)
	if err != nil {
		return err
	}
	logger.Info("migrations applied", "version", version)
	return nil
}

func seed(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	pool, err := storage.Open(ctx, storage.Config{URL: cfg.DatabaseURL})
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := tenant.Seed(ctx, tenant.NewRepository(pool)); err != nil {
		return err
	}
	logger.Info("demo tenant seeded", "tenant_id", tenant.DemoTenantID.String())
	return nil
}

// setupTracing creates spans without exporting them, so trace IDs propagate with no collector.
func setupTracing() func() {
	tp := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	return func() { _ = tp.Shutdown(context.Background()) }
}

// probeHealth exits 0 when the local server reports SERVING.
func probeHealth() int {
	addr := os.Getenv("TENANT_SVC_GRPC_ADDR")
	if addr == "" || addr[0] == ':' {
		addr = "127.0.0.1" + orDefault(addr, ":50051")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = conn.Close() }()

	resp, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		fmt.Fprintln(os.Stderr, errors.New(resp.GetStatus().String()))
		return 1
	}
	return 0
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
