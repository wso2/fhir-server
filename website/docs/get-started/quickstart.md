---
title: Quickstart
description: Start the server with Docker Compose and create your first FHIR resource.
---

# Run the server with Docker Compose

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

```bash
for i in $(seq 1 30); do
  curl -sf -m 5 http://localhost:9090/health/ready && break
  [ "$i" -eq 30 ] && { echo "server not ready after 60s; check docker compose logs" >&2; exit 1; }
  sleep 2
done
```

## Create a Patient

```bash
curl -sS -X POST http://localhost:9090/fhir/r4/Patient \
  -H "Content-Type: application/fhir+json" \
  -d '{
    "resourceType": "Patient",
    "name": [{"family": "Smith", "given": ["Alice"]}]
  }' | jq
```

The server assigns an `id`, sets version metadata, and returns the created Patient.

## Search for the Patient

```bash
curl -sS "http://localhost:9090/fhir/r4/Patient?family=Smith" | jq
```

The response is a FHIR searchset Bundle.

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
