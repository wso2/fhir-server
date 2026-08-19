---
title: Testing
description: Run unit, race, integration, and quality checks for the FHIR server.
---

# Test the server

Use the repository Make targets so local checks match continuous integration.

## Unit tests

Unit tests do not require PostgreSQL or Docker:

```bash
make test
```

The unit suite runs with the race detector. To invoke Go directly:

```bash
go test -race -count=1 ./...
```

## Integration tests

Integration tests require Docker and use the `integration` build tag:

```bash
make test-integration
```

They exercise PostgreSQL-backed behavior, including storage, search, transactions, history, and tenant isolation.

## Quality checks

```bash
make fmt
make vet
make lint
make test
```

Run integration tests when a change touches handlers, the store, indexing, search, schema, tenancy, or another database-backed path.

## Focused Go tests

```bash
go test -race -count=1 ./internal/fhirpath/...
go test -race -count=1 ./internal/handler/...
go test -race -count=1 ./internal/store/...
```

## Testing changes safely

- Add the smallest focused regression test that proves the behavior.
- Test both success and OperationOutcome failure paths.
- Include tenant separation cases for tenant-aware code.
- Verify index extraction and query behavior together when changing search.
- Test transaction rollback when changing Bundle processing.
- Use realistic partial precision, modifiers, and choice-type fields for FHIR search cases.

See [`TESTING.md`](https://github.com/wso2/fhir-server/blob/main/TESTING.md) for the repository's detailed testing model.
