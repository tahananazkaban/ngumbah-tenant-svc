-- name: GetTenant :one
SELECT id, name, plan FROM tenants WHERE id = $1;

-- name: UpsertTenant :exec
INSERT INTO tenants (id, name, plan)
VALUES ($1, $2, $3)
ON CONFLICT (id) DO UPDATE
SET name = EXCLUDED.name, plan = EXCLUDED.plan, updated_at = now()
WHERE tenants.name IS DISTINCT FROM EXCLUDED.name OR tenants.plan IS DISTINCT FROM EXCLUDED.plan;
