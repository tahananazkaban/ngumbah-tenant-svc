// Package server exposes the tenant service over gRPC with health, reflection and request logging.
package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"

	tenantv1 "github.com/tahananazkaban/ngumbah-tenant-svc/gen/ngumbah/tenant/v1"
	"github.com/tahananazkaban/ngumbah-tenant-svc/internal/tenant"
)

// Pinger reports whether the database is reachable.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Options wires the server's dependencies.
type Options struct {
	Tenants *tenant.Service
	Pinger  Pinger
	Logger  *slog.Logger
	// Reflection enables gRPC server reflection; development only.
	Reflection bool
	// HealthInterval is how often the database is pinged; zero means 5s.
	HealthInterval time.Duration
}

// Server is a gRPC server plus its background health checker.
type Server struct {
	grpc     *grpc.Server
	health   *health.Server
	pinger   Pinger
	interval time.Duration
	stop     chan struct{}
	once     sync.Once
}

// New builds the server; call Serve to start accepting requests.
func New(opts Options) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	interval := opts.HealthInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}

	gs := grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainUnaryInterceptor(logRequests(logger)),
	)
	tenantv1.RegisterTenantServiceServer(gs, &tenantHandler{svc: opts.Tenants, logger: logger})

	hs := health.NewServer()
	healthpb.RegisterHealthServer(gs, hs)
	if opts.Reflection {
		reflection.Register(gs)
	}

	s := &Server{grpc: gs, health: hs, pinger: opts.Pinger, interval: interval, stop: make(chan struct{})}
	s.setServing(false)
	return s
}

// Serve starts the health checker and blocks serving gRPC on lis.
func (s *Server) Serve(lis net.Listener) error {
	go s.watchHealth()
	return s.grpc.Serve(lis)
}

// GracefulStop finishes in-flight requests, then stops.
func (s *Server) GracefulStop() {
	s.once.Do(func() { close(s.stop) })
	s.health.Shutdown()
	s.grpc.GracefulStop()
}

// Stop closes all connections immediately.
func (s *Server) Stop() {
	s.once.Do(func() { close(s.stop) })
	s.grpc.Stop()
}

// watchHealth flips SERVING/NOT_SERVING based on database reachability.
func (s *Server) watchHealth() {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		ctx, cancel := context.WithTimeout(context.Background(), s.interval)
		s.setServing(s.pinger.Ping(ctx) == nil)
		cancel()
		select {
		case <-s.stop:
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) setServing(ok bool) {
	st := healthpb.HealthCheckResponse_NOT_SERVING
	if ok {
		st = healthpb.HealthCheckResponse_SERVING
	}
	// Empty name is the overall server status probed by container health checks
	s.health.SetServingStatus("", st)
	s.health.SetServingStatus(tenantv1.TenantService_ServiceDesc.ServiceName, st)
}

type tenantHandler struct {
	tenantv1.UnimplementedTenantServiceServer
	svc    *tenant.Service
	logger *slog.Logger
}

func (h *tenantHandler) GetTenant(ctx context.Context, req *tenantv1.GetTenantRequest) (*tenantv1.GetTenantResponse, error) {
	t, err := h.svc.Get(ctx, req.GetTenantId())
	if err != nil {
		return nil, h.toStatus(ctx, err)
	}
	return &tenantv1.GetTenantResponse{Tenant: &tenantv1.Tenant{
		Id:   t.ID.String(),
		Name: t.Name,
		Plan: toProtoPlan(t.Plan),
	}}, nil
}

// toStatus maps domain errors to gRPC codes and never exposes driver details.
func (h *tenantHandler) toStatus(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, tenant.ErrInvalidID):
		return status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	case errors.Is(err, tenant.ErrNotFound):
		return status.Error(codes.NotFound, "tenant not found")
	case errors.Is(err, tenant.ErrUnavailable), errors.Is(err, context.DeadlineExceeded):
		h.logger.ErrorContext(ctx, "tenant storage unavailable", append([]any{"error", err}, traceArgs(ctx)...)...)
		return status.Error(codes.Unavailable, "service temporarily unavailable")
	default:
		h.logger.ErrorContext(ctx, "get tenant failed", append([]any{"error", err}, traceArgs(ctx)...)...)
		return status.Error(codes.Internal, "internal error")
	}
}

func toProtoPlan(p tenant.Plan) tenantv1.Plan {
	switch p {
	case tenant.PlanFree:
		return tenantv1.Plan_PLAN_FREE
	case tenant.PlanPro:
		return tenantv1.Plan_PLAN_PRO
	default:
		return tenantv1.Plan_PLAN_UNSPECIFIED
	}
}

// logRequests writes one line per RPC with method, code, duration and trace IDs.
func logRequests(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := next(ctx, req)
		args := []any{
			"method", info.FullMethod,
			"code", status.Code(err).String(),
			"duration_ms", time.Since(start).Milliseconds(),
		}
		level := slog.LevelInfo
		// Health probes run every few seconds; keep them out of normal logs
		if strings.HasPrefix(info.FullMethod, "/grpc.health.v1.") {
			level = slog.LevelDebug
		}
		logger.Log(ctx, level, "rpc", append(args, traceArgs(ctx)...)...)
		return resp, err
	}
}

// traceArgs returns trace_id and span_id log fields when a span is active.
func traceArgs(ctx context.Context) []any {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return nil
	}
	return []any{"trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String()}
}
