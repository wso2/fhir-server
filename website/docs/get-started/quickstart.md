---
title: Quickstart
description: Start the server with Docker Compose and create your first FHIR resource.
---

# Run the server

The fastest way to a working server is the bundled Docker Compose stack: it starts PostgreSQL and
the FHIR server together, creates the schema on first boot, and needs no configuration. By the end
of this page you will have stored and searched your first FHIR resource.

To build the binary yourself, run against an existing PostgreSQL, or produce a container image, see
[Installation](./installation.md) instead.

## Prerequisites

- Docker Desktop, Docker Engine, or Colima
- `curl`
- `jq` for the response examples

## Start the stack

Clone the repository and start the stack from its root:

```bash
git clone https://github.com/wso2/fhir-server.git
cd fhir-server
docker compose up -d
```

`-d` runs the stack in the background so the same terminal can run the commands below. Use `docker compose logs -f` to follow the server output.

The stack starts:

| Service | Address |
| --- | --- |
| FHIR base URL | `http://localhost:9090/fhir/r4` |
| Readiness endpoint | `http://localhost:9090/health/ready` |
| Adminer | `http://localhost:8080` |
| PostgreSQL | `localhost:5432` |

Wait until readiness returns `200 OK`:

```bash title="Request"
for i in $(seq 1 30); do
  curl -sf -m 5 http://localhost:9090/health/ready && break
  [ "$i" -eq 30 ] && { echo "server not ready after the retry window; check docker compose logs" >&2; exit 1; }
  sleep 2
done
```

## Create a Patient

```bash title="Request"
curl -sS -X POST http://localhost:9090/fhir/r4/Patient \
  -H "Content-Type: application/fhir+json" \
  -d '{
    "resourceType": "Patient",
    "name": [{"family": "Smith", "given": ["Alice"]}]
  }' | jq
```

```json title="Response"
{
  "resourceType": "Patient",
  "id": "78969445-3b45-4e7e-86b5-31342dd99bf2",
  "meta": {"versionId": "1", "lastUpdated": "2026-09-03T06:39:29Z"},
  "name": [{"family": "Smith", "given": ["Alice"]}]
}
```

The server assigns an `id`, sets version metadata, and returns the created Patient.

## Search for the Patient

```bash title="Request"
curl -sS "http://localhost:9090/fhir/r4/Patient?family=Smith" | jq
```

```json title="Response"
{
  "resourceType": "Bundle",
  "type": "searchset",
  "entry": [
    {
      "fullUrl": "http://localhost:9090/fhir/r4/Patient/78969445-3b45-4e7e-86b5-31342dd99bf2",
      "resource": {
        "resourceType": "Patient",
        "id": "78969445-3b45-4e7e-86b5-31342dd99bf2",
        "meta": {"versionId": "1", "lastUpdated": "2026-09-03T06:39:29Z"},
        "name": [{"family": "Smith", "given": ["Alice"]}]
      },
      "search": {"mode": "match"}
    }
  ]
}
```

The response is a FHIR searchset Bundle. There is no `total` unless you ask for one with
`_total=accurate` — see [Search results and paging](../api/search-results.md#totals).

## Inspect PostgreSQL

Open `http://localhost:8080` and use:

| Field | Value |
| --- | --- |
| System | PostgreSQL |
| Server | `db` |
| Username | `fhir` |
| Password | `fhir` |
| Database | `fhirdb` |

:::warning
These credentials are local development defaults. Do not reuse them in a shared or production environment.
:::

## Stop and remove local data

```bash
docker compose down -v
```

Continue with [Installation](./installation.md) to run the binary directly or build a container image.
