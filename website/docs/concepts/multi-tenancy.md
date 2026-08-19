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

For example:

```bash
curl -sS "http://localhost:9090/t/acme/fhir/r4/Patient"
```

Set `BASE_URL` to the bare FHIR base URL without a tenant prefix. The server inserts `/t/{tenant}` into generated links (search Bundles, `Location` headers, the CapabilityStatement) automatically for tenant-prefixed requests.

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
