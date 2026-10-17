package server_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	tenantv1 "github.com/tahananazkaban/ngumbah-tenant-svc/gen/ngumbah/tenant/v1"
	"github.com/tahananazkaban/ngumbah-tenant-svc/internal/server"
	"github.com/tahananazkaban/ngumbah-tenant-svc/internal/storage"
	"github.com/tahananazkaban/ngumbah-tenant-svc/internal/tenant"
)

type harness struct {
	tenants tenantv1.TenantServiceClient
	health  healthpb.HealthClient
	repo    *tenant.Repository
}

// newHarness starts the real gRPC server in memory against a throwaway schema in tenant_db.
func newHarness(t *testing.T, dsn string) *harness {
	t.Helper()
	ctx := context.Background()

	schema := ""
	if dsn == "" {
		dsn = os.Getenv("TENANT_SVC_DATABASE_URL")
		if dsn == "" {
			t.Fatal("TENANT_SVC_DATABASE_URL is required for tests (see .env.example)")
		}
		schema = fmt.Sprintf("test_%s", strings.ReplaceAll(uuid.NewString(), "-", ""))
		admin, err := pgxpool.New(ctx, dsn)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
			t.Fatalf("create schema: %v", err)
		}
		t.Cleanup(func() {
			_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
			admin.Close()
		})
	}

	pool, err := storage.Open(ctx, storage.Config{URL: dsn, Schema: schema})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(pool.Close)

	if schema != "" {
		if err := storage.Migrate(ctx, pool); err != nil {
			t.Fatalf("migrate: %v", err)
		}
	}

	repo := tenant.NewRepository(pool)
	srv := server.New(server.Options{
		Tenants:        tenant.NewService(repo),
		Pinger:         pool,
		HealthInterval: 50 * time.Millisecond,
	})

	lis := bufconn.Listen(1 << 20)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return &harness{
		tenants: tenantv1.NewTenantServiceClient(conn),
		health:  healthpb.NewHealthClient(conn),
		repo:    repo,
	}
}

func callCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestGetTenantReturnsStoredTenant(t *testing.T) {
	h := newHarness(t, "")
	id := uuid.Must(uuid.NewV7())
	if err := h.repo.Upsert(callCtx(t), tenant.Tenant{ID: id, Name: "Laundry Bu Sari", Plan: tenant.PlanPro}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	resp, err := h.tenants.GetTenant(callCtx(t), &tenantv1.GetTenantRequest{TenantId: id.String()})
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	got := resp.GetTenant()
	if got.GetId() != id.String() || got.GetName() != "Laundry Bu Sari" || got.GetPlan() != tenantv1.Plan_PLAN_PRO {
		t.Fatalf("unexpected tenant: %v", got)
	}
}

func TestGetTenantMapsEachPlan(t *testing.T) {
	h := newHarness(t, "")
	cases := map[tenant.Plan]tenantv1.Plan{
		tenant.PlanFree: tenantv1.Plan_PLAN_FREE,
		tenant.PlanPro:  tenantv1.Plan_PLAN_PRO,
	}
	for plan, want := range cases {
		id := uuid.Must(uuid.NewV7())
		if err := h.repo.Upsert(callCtx(t), tenant.Tenant{ID: id, Name: "Plan " + string(plan), Plan: plan}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		resp, err := h.tenants.GetTenant(callCtx(t), &tenantv1.GetTenantRequest{TenantId: id.String()})
		if err != nil {
			t.Fatalf("GetTenant(%s): %v", plan, err)
		}
		if resp.GetTenant().GetPlan() != want {
			t.Errorf("plan %s: got %v, want %v", plan, resp.GetTenant().GetPlan(), want)
		}
	}
}

func TestGetTenantUnknownIDIsNotFound(t *testing.T) {
	h := newHarness(t, "")
	_, err := h.tenants.GetTenant(callCtx(t), &tenantv1.GetTenantRequest{TenantId: uuid.Must(uuid.NewV7()).String()})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("got %v, want NotFound", err)
	}
}

func TestGetTenantMalformedIDIsInvalidArgument(t *testing.T) {
	h := newHarness(t, "")
	for _, id := range []string{"", "not-a-uuid", "12345"} {
		_, err := h.tenants.GetTenant(callCtx(t), &tenantv1.GetTenantRequest{TenantId: id})
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("id %q: got %v, want InvalidArgument", id, err)
		}
	}
}

func TestGetTenantDatabaseDownIsUnavailableWithoutDetails(t *testing.T) {
	// Port 1 on localhost refuses connections, simulating a database outage
	h := newHarness(t, "postgres://tenant_svc:wrong@127.0.0.1:1/tenant_db?connect_timeout=1")
	_, err := h.tenants.GetTenant(callCtx(t), &tenantv1.GetTenantRequest{TenantId: uuid.Must(uuid.NewV7()).String()})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("got %v, want Unavailable", err)
	}
	msg := strings.ToLower(status.Convert(err).Message())
	for _, leak := range []string{"dial", "127.0.0.1", "connect", "pgx", "sql"} {
		if strings.Contains(msg, leak) {
			t.Errorf("message leaks %q: %q", leak, msg)
		}
	}
}

func TestHealthIsServingWithReachableDatabase(t *testing.T) {
	h := newHarness(t, "")
	waitForHealth(t, h, healthpb.HealthCheckResponse_SERVING)
}

func TestHealthIsNotServingWithUnreachableDatabase(t *testing.T) {
	h := newHarness(t, "postgres://tenant_svc:wrong@127.0.0.1:1/tenant_db?connect_timeout=1")
	waitForHealth(t, h, healthpb.HealthCheckResponse_NOT_SERVING)
}

func waitForHealth(t *testing.T, h *harness, want healthpb.HealthCheckResponse_ServingStatus) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last healthpb.HealthCheckResponse_ServingStatus
	for time.Now().Before(deadline) {
		resp, err := h.health.Check(callCtx(t), &healthpb.HealthCheckRequest{Service: tenantv1.TenantService_ServiceDesc.ServiceName})
		if err == nil {
			last = resp.GetStatus()
			if last == want {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("health: got %v, want %v", last, want)
}

func TestSeedIsIdempotent(t *testing.T) {
	h := newHarness(t, "")
	for range 2 {
		if err := tenant.Seed(callCtx(t), h.repo); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	resp, err := h.tenants.GetTenant(callCtx(t), &tenantv1.GetTenantRequest{TenantId: tenant.DemoTenantID.String()})
	if err != nil {
		t.Fatalf("GetTenant(demo): %v", err)
	}
	if resp.GetTenant().GetName() != "Laundry Demo" || resp.GetTenant().GetPlan() != tenantv1.Plan_PLAN_FREE {
		t.Fatalf("unexpected demo tenant: %v", resp.GetTenant())
	}
}
