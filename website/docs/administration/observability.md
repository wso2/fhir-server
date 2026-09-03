---
title: Observability
description: Scrape Prometheus metrics, read the structured logs, and decide what to alert on.
---

# Observability

The server emits Prometheus metrics and structured JSON logs. Neither carries resource content by
default, but both can carry identifiers — see the warning at the end of this page.

## Metrics

Metrics are exposed at `/metrics`, outside the FHIR base path so a scraper does not traverse the
FHIR middleware stack.

```bash title="Request"
curl -sS http://localhost:9090/metrics | grep fhir_
```

```text title="Response"
# HELP fhir_request_total Total number of FHIR HTTP requests.
# TYPE fhir_request_total counter
fhir_request_total{method="GET",route="/fhir/r4/Patient",status_code="200"} 1
fhir_request_total{method="GET",route="/fhir/r4/Patient/{id}",status_code="404"} 1
fhir_request_total{method="GET",route="/t/acme/fhir/r4/Patient",status_code="200"} 2
```

| Metric | Type | Labels |
| --- | --- | --- |
| `fhir_request_total` | counter | `method`, `route`, `status_code` |

The `route` label is the matched route pattern, not the raw URL, so ids are collapsed to `{id}` and
cardinality stays bounded. In a multi-tenant deployment the tenant prefix is part of the pattern
(`/t/acme/fhir/r4/Patient`), which lets you break traffic down per tenant.

Alongside it, `/metrics` exposes the standard Go runtime and process collectors — `go_goroutines`,
`go_memstats_*`, `go_gc_duration_seconds`, `process_resident_memory_bytes`, and so on — roughly 40
metric families in total.

:::note
`fhir_request_total` is the only FHIR-specific metric. There is **no** request-latency histogram, so
response-time monitoring has to come from your ingress, service mesh, or APM agent rather than from
this endpoint.
:::

A minimal scrape config:

```yaml
scrape_configs:
  - job_name: fhir-server
    metrics_path: /metrics
    static_configs:
      - targets: ['fhir-server:9090']
```

## Structured logs

The server writes JSON to standard output. Set verbosity with `LOG_LEVEL` (`debug`, `info`, `warn`,
`error`; default `info`) — see [Configuration](./configuration.md).

```bash title="Request"
LOG_LEVEL=info ./fhir-server
```

```json title="Response"
{"time":"2026-09-03T08:55:01Z","level":"INFO","msg":"server listening","addr":":9090","baseURL":"http://localhost:9090/fhir/r4"}
```

Startup logs are worth capturing on every deploy, because they report the **effective**
configuration after environment overrides are applied:

```json title="Response"
{"level":"INFO","msg":"search tuning","probeCap":5000,"defaultPageSize":20,"maxPageSize":0,"maxChainDepth":5,"planCacheMode":"force_custom_plan"}
{"level":"INFO","msg":"write tuning","maxRowsPerStatement":1000,"maxRowsPerBundle":100000}
{"level":"INFO","msg":"FHIR router initialized","baseURL":"http://localhost:9090/fhir/r4","validateOnWrite":false,"baseValidation":true,"igPackages":0}
```

Confirm those logged values match what you intended rather than assuming an environment variable
took effect.

## What to monitor

| Signal | Where it comes from | Why |
| --- | --- | --- |
| Request volume and status classes | `fhir_request_total` | Detect error spikes and traffic shifts |
| Request latency | Ingress or APM | Not exposed by this server |
| Readiness state and restart count | `/health/ready`, orchestrator | Distinguish crash loops from slow startup |
| PostgreSQL connections, lock waits, statement latency, disk, autovacuum | Database monitoring | The database is the capacity limit for search and writes |
| Transaction Bundle duration and rejection counts | Logs, `fhir_request_total` by status | Large Bundles are the most common timeout source |
| Implementation Guide load failures | Startup logs | A failed package silently reduces validation coverage |
| Terminology latency and failures | Logs | Terminology-backed search modifiers fail closed |
| Cross-tenant access-denial checks | Your own probes | Verify isolation continuously in shared deployments |

## Correlation

Preserve a request correlation identifier at the ingress and propagate it, so a single FHIR request
can be traced across the gateway, the server logs, and the database.

:::warning[PHI]
FHIR resources and search parameters can contain protected health information. Search values may
appear in URLs, and URLs commonly reach access logs, traces, and metrics labels. Apply data
minimisation and access controls to every telemetry sink, and avoid logging request or response
bodies by default.
:::
