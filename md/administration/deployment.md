---
title: Deployment
description: Best practices for running the server and PostgreSQL in production.
---

# Deployment best practices

The server ships as a Go binary and container image. A production deployment also needs PostgreSQL, schema provisioning, ingress and identity controls, backups, secrets, monitoring, and a deliberate tenant model. The practices below cover each of those.

## Deployment checklist

1. Build an immutable binary or container from a reviewed commit.
2. Provision a supported PostgreSQL version and apply `internal/db/schema.sql` with a controlled DDL role.
3. Create a least-privileged runtime database role.
4. Store database credentials and other secrets outside the image and repository.
5. Set `BASE_URL` to the canonical externally reachable FHIR base URL.
6. Configure read, write, idle, client, ingress, and database timeouts coherently.
7. Put TLS and authenticated authorization enforcement in front of the service.
8. Configure liveness (`/health/live`) and readiness (`/health/ready`) probes separately.
9. Establish backup, restore, retention, and disaster-recovery procedures.
10. Run smoke, search, tenancy, and restore tests before accepting traffic.

## Container

Tag images with a reviewed commit or release version rather than a mutable tag, and deploy by immutable digest:

```bash
docker build -t "fhir-server:$(git rev-parse --short HEAD)" .
```

Pass runtime configuration through environment variables or mount a YAML configuration file. Do not bake credentials into the image.

## Database provisioning

Apply the schema separately:

```bash
psql "$DATABASE_URL" -f internal/db/schema.sql
```

`FHIR_CREATE_TABLES=true` is intended for controlled first-start or local workflows, not as a default runtime privilege.

## Network and identity boundary

The FHIR server handles FHIR resources and tenant-scoped storage. Deploy an API gateway, service mesh, or equivalent enforcement point for:

- TLS termination and certificate policy.
- Authentication and token validation.
- Tenant binding and authorization.
- Rate limits and abuse controls.
- Network allowlists and request-size controls.
- Security audit integration.

## Timeouts

`SERVER_WRITE_TIMEOUT` bounds the entire handler execution from the HTTP server's perspective. Large transaction Bundles can exceed the default. Measure the largest supported request under expected concurrency, then coordinate the server, proxy, client, and database timeout budgets.

:::warning
A client-side EOF during a long transaction Bundle can represent an indeterminate outcome. Reconcile resource state before retrying.
:::

## After bulk loading

Run `VACUUM (ANALYZE)` on `resources` and on every search-parameter table after a bulk import, executing each statement outside a transaction. This refreshes visibility maps and planner statistics before serving search traffic. See [Performance tuning](https://github.com/wso2/fhir-server/blob/main/docs/performance-tuning.md) for the full procedure.
