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
10. For multi-replica deployments, set `SEARCH_PARAM_WATCH=true` so search-parameter changes reach every replica.
11. Run smoke, search, tenancy, and restore tests before accepting traffic.

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

`SERVER_WRITE_TIMEOUT` is a deadline on the HTTP connection, not on the work: when it expires,
Go closes the connection but does **not** cancel the handler or its request context — the
handler runs to completion, so a transaction Bundle may still commit after the client has seen a
bare `EOF` (not a `504`). Large transaction Bundles can exceed the default. Measure the largest
supported request under expected concurrency, then coordinate the server, proxy, client, and
database timeout budgets — and keep the server timeout above the client's, so the client governs
abandonment.

:::warning
A client-side `EOF` during a long transaction Bundle is an **indeterminate** outcome: the
database result is independent of the HTTP result, so the transaction may have committed after
the connection closed. Reconcile resource state before retrying — an unconditional retry may
apply the bundle twice.
:::

## Multiple replicas

Search-parameter definitions live in each process. Enable `SEARCH_PARAM_WATCH` (off by default) when
running more than one replica: the replicas then keep those definitions in sync over PostgreSQL
`LISTEN/NOTIFY`, so a `SearchParameter` created on one replica reaches the others and keeps their
write-time search indexing consistent. A single-node deployment needs no setting change.

Propagation is best-effort and eventual — a replica learns of a change when it receives the
notification or reconnects, with no fixed lag bound — but it does not backfill resources already
written without the new parameter. See
[Custom search parameters](../api/search.md#custom-search-parameters).

## After bulk loading

Run `VACUUM (ANALYZE)` on `resources` and on every search-parameter table after a bulk import, executing each statement outside a transaction. This refreshes visibility maps and planner statistics before serving search traffic. See [Performance tuning](https://github.com/wso2/fhir-server/blob/main/docs/performance-tuning.md) for the full procedure.
