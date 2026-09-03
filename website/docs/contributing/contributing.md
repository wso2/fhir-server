---
title: Contributing
description: Prepare focused, tested contributions to WSO2 FHIR Server.
---

# Contribute to WSO2 FHIR Server

## Prepare the repository

```bash
git clone https://github.com/wso2/fhir-server.git
cd fhir-server
make build
make test
```

Required development tools include Go 1.25 or later, PostgreSQL 14 or later, and [golangci-lint](https://golangci-lint.run) for `make lint`. Docker can provide the database used by integration tests.

## Development workflow

1. Fork the repository and branch from an up-to-date `main`.
2. Keep the change focused and add regression coverage.
3. Run formatting, vet, lint, and unit tests.
4. Run integration tests for database-backed behavior.
5. Open a pull request against `main` and complete the repository template.

## Code conventions

- Format Go with `gofmt`.
- Wrap errors with context using `fmt.Errorf("...: %w", err)`.
- Include the repository's Apache 2.0 header in new Go files.
- Keep comments focused on non-obvious reasoning.
- Place unit tests next to the code under test.
- Use the `integration` build tag for PostgreSQL integration tests.

## Before opening a pull request

```bash
make fmt
make vet
make lint
make test
make test-integration
```

Document any check that could not be run and why. Do not report security vulnerabilities through a public issue; follow [`SECURITY.md`](https://github.com/wso2/fhir-server/blob/main/SECURITY.md).

The complete contribution policy is in [`CONTRIBUTING.md`](https://github.com/wso2/fhir-server/blob/main/CONTRIBUTING.md).
