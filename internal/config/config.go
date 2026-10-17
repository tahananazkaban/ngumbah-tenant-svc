// Package config loads tenant-svc settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// DefaultGRPCAddr is the listen address when TENANT_SVC_GRPC_ADDR is unset.
const DefaultGRPCAddr = ":50051"

// GRPCAddr returns the configured listen address without validating anything else.
func GRPCAddr() string { return getenv("TENANT_SVC_GRPC_ADDR", DefaultGRPCAddr) }

// Config holds every runtime setting of the service.
type Config struct {
	GRPCAddr      string
	DatabaseURL   string
	Env           string
	LogLevel      slog.Level
	TraceExporter string
}

// Development reports whether development-only behavior (reflection, auto-migrate) is on.
func (c Config) Development() bool { return c.Env == "development" }

// Load reads the environment and validates it; all problems are reported at once.
func Load() (Config, error) {
	cfg := Config{
		GRPCAddr:      GRPCAddr(),
		DatabaseURL:   os.Getenv("TENANT_SVC_DATABASE_URL"),
		Env:           getenv("TENANT_SVC_ENV", "production"),
		TraceExporter: getenv("OTEL_TRACES_EXPORTER", "none"),
	}

	var errs []error
	if cfg.DatabaseURL == "" {
		errs = append(errs, errors.New("TENANT_SVC_DATABASE_URL is required"))
	}
	if cfg.Env != "development" && cfg.Env != "production" {
		errs = append(errs, fmt.Errorf("TENANT_SVC_ENV must be development or production, got %q", cfg.Env))
	}
	if err := cfg.LogLevel.UnmarshalText([]byte(getenv("TENANT_SVC_LOG_LEVEL", "info"))); err != nil {
		errs = append(errs, fmt.Errorf("TENANT_SVC_LOG_LEVEL: %w", err))
	}
	if cfg.TraceExporter != "none" {
		errs = append(errs, fmt.Errorf("OTEL_TRACES_EXPORTER %q is not supported yet; use none", cfg.TraceExporter))
	}
	return cfg, errors.Join(errs...)
}

func getenv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
