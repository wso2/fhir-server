<!--
Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com).

WSO2 LLC. licenses this file to you under the Apache License,
Version 2.0 (the "License"); you may not use this file except
in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing,
software distributed under the License is distributed on an
"AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
KIND, either express or implied. See the License for the
specific language governing permissions and limitations
under the License.
-->

# fhir-server Helm chart

Deploys the [WSO2 FHIR Server](../README.md) — a FHIR R4 REST server backed by
PostgreSQL — to Kubernetes. The chart ships a single `Deployment` + `Service`
(plus optional `Ingress`/`HorizontalPodAutoscaler`/`PodDisruptionBudget`/
`NetworkPolicy`/`PersistentVolumeClaim`); the server itself is stateless
besides its database and an optional local IG-package cache.

## Before you install: database & schema

This chart **never** provisions PostgreSQL and **never** creates the schema —
both are out-of-band, one-time steps, run against an admin/DDL-capable role,
*before* `helm install`:

```bash
# 1. Create the database and a least-privilege runtime role (see the main
#    README's "Multi-Tenancy" section for the full statement set). The role
#    the chart's DATABASE_URL points at must NOT be a superuser and must NOT
#    have BYPASSRLS -- either silently disables tenant isolation.
psql "$ADMIN_DATABASE_URL" -c "CREATE ROLE fhir_app LOGIN PASSWORD '...';"

# 2. Apply the schema (idempotent, safe to re-run on upgrade).
psql "$ADMIN_DATABASE_URL" -f ../internal/db/schema.sql

# 3. Create a Secret the chart will reference (recommended path -- see below).
kubectl create secret generic fhir-server-db-credentials \
  --from-literal=database-url='postgres://fhir_app:...@host:5432/fhirdb?sslmode=require'
```

## Installing

```bash
helm install fhir-server ./helm \
  --set database.existingSecret.name=fhir-server-db-credentials \
  --set database.existingSecret.key=database-url
```

For local/throwaway use only, `database.url` can be set directly instead —
this renders a chart-managed `Secret`, so the DSN ends up in `values` and in
Helm release history; prefer `existingSecret` for anything else.

## Values

| Key | Default | Description |
|---|---|---|
| `replicaCount` | `1` | Pod replicas (ignored when `autoscaling.enabled`). |
| `image.repository` | `ghcr.io/wso2/fhir-server` | Image repository, registry host included. |
| `image.tag` | `v2.0.0` | Image tag (released tags carry a `v` prefix, unlike `Chart.yaml`'s `appVersion`). |
| `image.digest` | `""` | Image digest (`sha256:...`), takes precedence over `tag`. |
| `image.pullPolicy` | `IfNotPresent` | |
| `imagePullSecrets` | `[]` | Names of pre-existing pull secrets. |
| `serviceAccount.create` | `true` | |
| `serviceAccount.name` | `""` | |
| `serviceAccount.annotations` | `{}` | e.g. IRSA / Workload Identity annotations. |
| `podSecurityContext` | nonroot, seccomp `RuntimeDefault` | Pod-level `securityContext`. |
| `containerSecurityContext` | `readOnlyRootFilesystem: true`, all caps dropped | Container-level `securityContext`. |
| `service.type` | `ClusterIP` | |
| `service.port` | `9090` | Also used as `SERVER_PORT` inside the container. |
| `ingress.enabled` | `false` | |
| `ingress.className` / `.annotations` / `.hosts` / `.tls` | — | Standard `networking.k8s.io/v1` Ingress fields. |
| `resources` | `100m/128Mi` request, `500m/256Mi` limit | Sized for a single Go binary, not a JVM workload. |
| `autoscaling.enabled` | `false` | Toggles a `HorizontalPodAutoscaler`. |
| `podDisruptionBudget.enabled` | `false` | |
| `networkPolicy.enabled` | `false` | Default egress only opens DNS — add the database (and IG registry / terminology server, if used) via `networkPolicy.extraEgress`. |
| `persistence.igCache.enabled` | `false` | Persist `IG_CACHE_DIR` across restarts via a PVC; otherwise an `emptyDir` is used (packages just re-download). |
| `database.url` | `""` | Dev/quickstart-only literal DSN. |
| `database.existingSecret.name` / `.key` | `""` / `database-url` | Recommended production path. |
| `config.env` | `{LOG_LEVEL: info}` | Free-form env vars — covers every knob in [`config.example.yaml`](../config.example.yaml) (`BASE_URL`, `IG_PACKAGES`, `IG_REGISTRY_URL`, `SEARCH_PROBE_CAP`, `FHIR_VALIDATION_*`, `FHIR_TERMINOLOGY_URL`, `WRITE_*`, ...). |
| `extraEnv` / `extraEnvFrom` / `extraVolumes` / `extraVolumeMounts` | `[]` | Escape hatches for anything not covered above. |
| `probes.startup` / `.liveness` / `.readiness` | see `values.yaml` | Wired to `GET /health/live` and `GET /health/ready`. |
| `nodeSelector` / `tolerations` / `affinity` / `topologySpreadConstraints` | `{}` / `[]` | Raw passthroughs. |

See `values.yaml` for the full set and inline documentation.

## What this chart deliberately does not do

- No bundled PostgreSQL — the database is always external and pre-provisioned.
- No schema migration Job or Helm hook — see "Before you install" above.
- No Gateway API `HTTPRoute` or OpenShift `Route` — Ingress only.

## Verifying an install

```bash
helm test fhir-server
kubectl port-forward svc/fhir-server 9090:9090
curl http://localhost:9090/health/ready
curl http://localhost:9090/fhir/r4/metadata | jq '.implementationGuide'
```
