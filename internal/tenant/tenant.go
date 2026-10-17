// Package tenant holds the tenant domain: lookup rules, errors and persistence.
package tenant

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tahananazkaban/ngumbah-tenant-svc/internal/storage/sqlcgen"
)

// Plan is the subscription plan marker; there is no billing yet.
type Plan string

// Known plans, matching the database check constraint.
const (
	PlanFree Plan = "free"
	PlanPro  Plan = "pro"
)

// Tenant is one laundry business using Ngumbah.
type Tenant struct {
	ID   uuid.UUID
	Name string
	Plan Plan
}

var (
	// ErrInvalidID means the caller sent something that is not a UUID.
	ErrInvalidID = errors.New("tenant id must be a UUID")
	// ErrNotFound means no tenant has the requested ID.
	ErrNotFound = errors.New("tenant not found")
	// ErrUnavailable means the database could not be reached in time.
	ErrUnavailable = errors.New("tenant storage unavailable")
)

// DemoTenantID is the fixed ID of the seeded demo tenant used by the gateway, web app and smoke test.
var DemoTenantID = uuid.MustParse("019a0000-0000-7000-8000-000000000001")

// Repository reads and writes tenants in tenant_db.
type Repository struct {
	q *sqlcgen.Queries
}

// NewRepository wraps a pool whose search_path points at the tenants table.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{q: sqlcgen.New(pool)}
}

// Get returns the tenant with the given ID.
func (r *Repository) Get(ctx context.Context, id uuid.UUID) (Tenant, error) {
	row, err := r.q.GetTenant(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Tenant{}, ErrNotFound
		}
		return Tenant{}, storageError(err)
	}
	return Tenant{ID: row.ID, Name: row.Name, Plan: Plan(row.Plan)}, nil
}

// Upsert creates the tenant or updates its name and plan.
func (r *Repository) Upsert(ctx context.Context, t Tenant) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	err := r.q.UpsertTenant(ctx, sqlcgen.UpsertTenantParams{ID: t.ID, Name: t.Name, Plan: string(t.Plan)})
	if err != nil {
		return storageError(err)
	}
	return nil
}

// storageError wraps connection-level failures as ErrUnavailable; anything else stays an internal error.
func storageError(err error) error {
	if isUnreachable(err) {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return fmt.Errorf("tenant storage: %w", err)
}

// isUnreachable reports timeouts, failed connects, network errors and connection-class SQLSTATEs.
func isUnreachable(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return isConnectionCode(pgErr.Code)
	}
	var connErr *pgconn.ConnectError
	var netErr net.Error
	return errors.Is(err, context.DeadlineExceeded) || pgconn.Timeout(err) ||
		errors.As(err, &connErr) || errors.As(err, &netErr)
}

// isConnectionCode covers SQLSTATE classes 08 (connection), 53 (resources) and 57P (shutdown).
func isConnectionCode(code string) bool {
	return strings.HasPrefix(code, "08") || strings.HasPrefix(code, "53") || strings.HasPrefix(code, "57P")
}

// queryTimeout bounds every database call made by the service.
const queryTimeout = 2 * time.Second

// Service applies the tenant lookup rules on top of the repository.
type Service struct {
	repo *Repository
}

// NewService builds the tenant service.
func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

// Get parses the raw ID and returns the tenant.
func (s *Service) Get(ctx context.Context, rawID string) (Tenant, error) {
	id, err := uuid.Parse(rawID)
	if err != nil {
		return Tenant{}, ErrInvalidID
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	return s.repo.Get(ctx, id)
}

// Seed creates or refreshes the demo tenant; safe to run repeatedly.
func Seed(ctx context.Context, repo *Repository) error {
	return repo.Upsert(ctx, Tenant{ID: DemoTenantID, Name: "Laundry Demo", Plan: PlanFree})
}
