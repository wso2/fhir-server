---
title: Health and observability
description: Monitor startup, readiness, logs, and critical dependencies.
---

# Health and observability

Use separate liveness and readiness signals so an orchestrator can distinguish a running process from an instance ready to accept FHIR traffic.

## Health endpoints

Check liveness (the process is up and serving HTTP):

```bash
curl -i http://localhost:9090/health/live
```

Check readiness (startup work has completed):

```bash
curl -i http://localhost:9090/health/ready
```

A `200 OK` readiness response indicates startup has completed, including Implementation Guide package loading, which runs in the background and can keep readiness closed after the listener is available while liveness already returns `200 OK`. Readiness does not probe PostgreSQL or other runtime dependencies on each request; monitor database availability separately.

## Structured logs

The server writes JSON logs to standard output. Configure verbosity with `LOG_LEVEL`:

```bash
LOG_LEVEL=info ./fhir-server
```

The listening log includes the effective address and FHIR base URL:

```json
{"level":"INFO","msg":"server listening","addr":":9090","baseURL":"http://localhost:9090/fhir/r4"}
```

Startup logs also report effective search tuning. Confirm the logged values when applying environment overrides.

## What to monitor

- HTTP request volume, latency, response classes, and OperationOutcome error codes.
- Readiness state and restart frequency.
- PostgreSQL connections, lock waits, statement latency, replication, disk, and autovacuum.
- Search latency by resource type and parameter class.
- Transaction Bundle duration and rejection counts.
- Implementation Guide load failures.
- External terminology latency and failures.
- Cross-tenant access-denial tests in shared deployments.

## Correlation

Preserve request correlation identifiers at the ingress and include them in downstream logs where the deployment stack supports it. Avoid logging resource bodies or sensitive query values by default.

:::warning
FHIR resources and search parameters can contain protected health information. Apply data minimization and access controls to logs, traces, metrics labels, and error reporting systems.
:::
