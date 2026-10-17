# ngumbah-tenant-svc

Tenant service for Ngumbah. Serves `ngumbah.tenant.v1.TenantService` from [ngumbah-contracts](https://github.com/tahananazkaban/ngumbah-contracts) `v0.1.0` over gRPC, backed by its own `tenant_db` in Postgres.

## Run locally

Start the shared infrastructure from `ngumbah-platform` first (`make infra-up`), then:

```sh
make env       # create .env from .env.example
make migrate   # apply migrations to tenant_db
make seed      # create the demo tenant
make run       # serve on :50051 with live reload
```

The demo tenant has the fixed ID `019a0000-0000-7000-8000-000000000001` ("Laundry Demo", free plan).

Call it with `buf curl` (reflection is on in development):

```sh
buf curl --protocol grpc --http2-prior-knowledge \
  -d '{"tenant_id":"019a0000-0000-7000-8000-000000000001"}' \
  http://localhost:50051/ngumbah.tenant.v1.TenantService/GetTenant
```

## Commands

| Command | What it does |
|---|---|
| `make build` | Build `bin/tenant-svc` |
| `make run` | Serve with live reload |
| `make test` | Run tests against `tenant_db` (each run uses a throwaway schema) |
| `make lint` | golangci-lint (in Docker) |
| `make generate` | Regenerate gRPC code from the contract tag and sqlc code from SQL |
| `make generate-check` | Fail if committed generated code is out of date |
| `make migrate` / `make seed` | Apply migrations / seed the demo tenant |
| `make docker-build` | Build the container image |

The binary has four subcommands: `serve` (default), `migrate`, `seed`, and `health` (gRPC health probe used by the container).

## Configuration

| Variable | Default | Notes |
|---|---|---|
| `TENANT_SVC_GRPC_ADDR` | `:50051` | Listen address |
| `TENANT_SVC_DATABASE_URL` | required | Connects as `tenant_svc` to `tenant_db` |
| `TENANT_SVC_ENV` | `production` | `development` enables reflection and auto-migrate on start |
| `TENANT_SVC_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `OTEL_TRACES_EXPORTER` | `none` | Spans are created and propagated but not exported |

## Behavior

| Situation | gRPC status |
|---|---|
| Tenant found | `OK` |
| `tenant_id` empty or not a UUID | `INVALID_ARGUMENT` |
| No tenant with that ID | `NOT_FOUND` |
| Database unreachable or too slow (2 s per query) | `UNAVAILABLE` |
| Anything else | `INTERNAL` |

Error messages never include database details. Health reports `SERVING` only while the database answers pings.

Logs are JSON, one line per RPC with `method`, `code`, `duration_ms`, `trace_id` and `span_id`. The trace ID continues the caller's W3C `traceparent`.

## Upgrading the contract

Change `CONTRACTS_TAG` in the `Makefile`, run `make generate`, and commit the result.
