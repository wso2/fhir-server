---
title: Implementation Guides
description: Load FHIR packages and expose their profiles through the server.
---

# FHIR Implementation Guides

The server can load FHIR npm packages at startup. Loaded packages contribute profiles and search definitions used by validation and the generated CapabilityStatement.

## Configure packages

In `config.yaml`:

```yaml
ig:
  packages:
    - hl7.fhir.us.core@6.1.0
    - hl7.fhir.us.carin-bb@2.0.0
  registryUrl: https://packages.fhir.org
  forceReload: false
  cacheDir: .fhir-ig-cache
```

Or use environment variables:

```bash
export IG_PACKAGES="hl7.fhir.us.core@6.1.0,hl7.fhir.us.carin-bb@2.0.0"
export IG_REGISTRY_URL="https://packages.fhir.org"
```

Package entries may use `name@version` or a direct `.tgz` URL.

## Startup behavior

For each configured package, the loader:

1. Resolves and downloads the package when it is not cached.
2. Extracts relevant StructureDefinitions and SearchParameters.
3. Stores package and profile metadata in PostgreSQL.
4. Updates the in-memory registries used by validation and search.

Previously loaded packages are skipped unless `IG_FORCE_RELOAD=true`.

## Verify loaded profiles

Read the server CapabilityStatement:

```bash
curl -sS http://localhost:9090/fhir/r4/metadata | jq
```

Loaded packages appear as canonical URLs in `CapabilityStatement.implementationGuide`; supported profiles and search parameters appear under the per-type entries in `rest[0].resource`:

```bash
curl -sS http://localhost:9090/fhir/r4/metadata | jq '.implementationGuide'
```

:::tip
Pin package versions. An unpinned package source makes startup behavior and validation rules harder to reproduce across environments.
:::
