---
title: Health checks
description: Wire liveness and readiness probes so an orchestrator can tell a running process from a ready one.
---

# Health checks

The server exposes two independent probes. Use both: they answer different questions, and treating
them as one causes an orchestrator either to kill a healthy instance or to send traffic to one that
cannot serve it yet.

| Endpoint | Question it answers | Use it for |
| --- | --- | --- |
| `/health/live` | Is the process up and serving HTTP? | Liveness probe — restart the container when this fails |
| `/health/ready` | Has startup work finished, so FHIR traffic can be served? | Readiness probe — add or remove the instance from the load balancer |

Both are outside the FHIR base path, so they need no FHIR content type and are unaffected by
tenancy.

## Liveness

```bash title="Request"
curl -i http://localhost:9090/health/live
```

```text title="Response"
HTTP/1.1 200 OK
Content-Length: 0
```

It returns `200` as soon as the HTTP listener is accepting connections. The response has no body.

## Readiness

```bash title="Request"
curl -i http://localhost:9090/health/ready
```

```text title="Response"
HTTP/1.1 200 OK
Content-Length: 0
```

Readiness turns `200` only once startup work has completed — including
[Implementation Guide](../conformance/implementation-guides.md) package loading, which runs in the
background. On a deployment that loads large packages, expect a window where liveness already
returns `200` while readiness is still `503`. That is the intended behaviour: the process is alive
but must not receive traffic yet.

:::warning
Readiness does **not** probe PostgreSQL or the terminology server on each call. A `200` means
startup finished, not that every dependency is currently healthy. Monitor database and terminology
availability separately — see [Observability](./observability.md).
:::

## Wiring probes in Kubernetes

```yaml
livenessProbe:
  httpGet:
    path: /health/live
    port: 9090
  periodSeconds: 10
  failureThreshold: 3

readinessProbe:
  httpGet:
    path: /health/ready
    port: 9090
  periodSeconds: 5
  failureThreshold: 2
```

Give readiness a startup allowance long enough to cover IG loading, either with a generous
`failureThreshold` or a `startupProbe` against `/health/ready`.

:::note
The container image is distroless and has no shell or `wget`, so a `CMD-SHELL`-style Docker
healthcheck inside the container will not work. Probe it externally — from the orchestrator, or from
the host with `curl -fsS http://localhost:9090/health/ready`.
:::
