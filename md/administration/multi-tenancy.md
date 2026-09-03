---
title: Multi-tenancy
description: Choose between physically isolated and logically isolated tenant deployments.
---

# Choose an isolation boundary

WSO2 FHIR Server supports two deployment patterns. Choose the isolation boundary before provisioning data or exposing client URLs.

## Dedicated server and database

Run one server and one PostgreSQL database for each tenant.

**Use this model when:**

- Regulatory or contractual requirements demand physical separation.
- Tenants need independent scaling, maintenance, backup, or retention policies.
- Operational simplicity is more important than infrastructure consolidation.

The FHIR base URL does not require a tenant path:

```text
https://tenant.example.com/fhir/r4
```

## Shared server and database

Run one service and database while scoping data with a tenant identifier and PostgreSQL row-level security.

No configuration flag is needed: the server always mounts both the bare FHIR base path and the tenant-prefixed routes, and the row-level security policies are part of the standard schema. Requests to the bare path use the default tenant scope; requests under a tenant prefix are scoped to that tenant.

Tenant-aware routes use:

```text
/t/{tenant}/fhir/r4
```

Set `BASE_URL` to the bare FHIR base URL, without a tenant prefix. The server inserts `/t/{tenant}` into generated links (search Bundles, `Location` headers, the CapabilityStatement) automatically for tenant-prefixed requests.

## Enable and use a tenant

There is nothing to enable and no tenant to register. Every FHIR route is already mounted under
`/t/{tenant}`, and a tenant begins to exist the first time you write to its path. The walkthrough
below uses two tenants, `acme` and `beta`, on a stock deployment.

**1. Write into a tenant.** Use the tenant path instead of the bare base path:

```bash title="Request"
curl -i -X POST "http://localhost:9090/t/acme/fhir/r4/Patient" \
  -H "Content-Type: application/fhir+json" \
  -d '{"resourceType": "Patient", "name": [{"family": "Acme-Patient"}]}'
```

The `Location` header comes back tenant-scoped, so a client can follow it without knowing the
tenancy rules:

```text title="Response headers"
HTTP/1.1 201 Created
ETag: W/"1"
Location: http://localhost:9090/t/acme/fhir/r4/Patient/ef8e696f-cb28-4a2d-859a-8ffbc8868249/_history/1
```

**2. Read it back inside the same tenant.**

```bash title="Request"
curl -s -o /dev/null -w "%{http_code}\n" \
  "http://localhost:9090/t/acme/fhir/r4/Patient/ef8e696f-cb28-4a2d-859a-8ffbc8868249"
```

```text title="Response"
200
```

**3. Confirm the boundary holds.** The same id is not readable from another tenant, nor from the
default scope — the row-level security policy filters it out, so the server answers `404` rather
than revealing that the id exists elsewhere:

```bash title="Request"
# same resource id, different scopes
curl -s -o /dev/null -w "beta:    %{http_code}\n" \
  "http://localhost:9090/t/beta/fhir/r4/Patient/ef8e696f-cb28-4a2d-859a-8ffbc8868249"
curl -s -o /dev/null -w "default: %{http_code}\n" \
  "http://localhost:9090/fhir/r4/Patient/ef8e696f-cb28-4a2d-859a-8ffbc8868249"
```

```text title="Response"
beta:    404
default: 404
```

Searches are scoped the same way:

```bash title="Request"
for scope in "/t/acme" "/t/beta" ""; do
  printf "%-8s " "${scope:-default}"
  curl -sS "http://localhost:9090${scope}/fhir/r4/Patient?_total=accurate" | jq '.total'
done
```

```text title="Response"
/t/acme  2
/t/beta  0
default  0
```

**4. Check the generated links.** Bundle navigation links stay inside the tenant, so paging a
tenant-scoped search never escapes its scope:

```bash title="Request"
curl -sS "http://localhost:9090/t/acme/fhir/r4/Patient?_count=1" | jq '[.link[].url]'
```

```json title="Response"
[
  "http://localhost:9090/t/acme/fhir/r4/Patient?_count=1&_page=1",
  "http://localhost:9090/t/acme/fhir/r4/Patient?_count=1&_page=1",
  "http://localhost:9090/t/acme/fhir/r4/Patient?_count=1&_page=2"
]
```

The tenant's [CapabilityStatement](../api/capability-statement.md) is served the same way, at
`/t/acme/fhir/r4/metadata`.

:::note
Requests to the bare base path are not rejected — they run in the **default** tenant scope. Treat
that scope as one more tenant: on a shared deployment, either route all traffic through a tenant
prefix at the gateway, or accept that the bare path is its own isolated dataset.
:::

:::warning
A URL tenant identifier is routing context, not proof of identity or authorization. Place an authenticated gateway or equivalent security layer in front of the service and bind the authenticated tenant to the routed tenant value.
:::

## Selection guide

| Concern | Dedicated | Shared |
| --- | --- | --- |
| Data isolation | Infrastructure boundary | Database policy boundary |
| Scaling | Per tenant | Shared capacity |
| Upgrades | Independent | Coordinated |
| Backup and restore | Per tenant | Requires tenant-aware procedures |
| Operational overhead | Higher | Lower |

Validate backups, connection pooling, privileged database access, and cross-tenant negative tests for the selected model.
