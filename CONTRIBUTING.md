# Contributing to WSO2 FHIR Server

Thanks for your interest in contributing! This document explains how to set up
your environment, the conventions we follow, and how to submit changes.

## Getting started

**Prerequisites:** Go 1.25+, PostgreSQL 13+ (or Docker for the integration tests).

```bash
git clone https://github.com/wso2/fhir-server.git
cd fhir-server
make build      # compile the server
make test       # run unit tests
```

See the [README](README.md) for how to run the server and configure it.

## Development workflow

1. Fork the repository and create a topic branch off `main`.
2. Make your change, keeping commits focused and well-described.
3. Add or update tests (see [TESTING.md](TESTING.md)).
4. Make sure everything is green locally before opening a PR:

   ```bash
   make fmt     # gofmt
   make vet     # go vet
   make lint    # golangci-lint
   make test    # unit tests (race detector)
   ```

   If your change touches the store, handlers, or schema, also run:

   ```bash
   make test-integration   # requires Docker
   ```

5. Open a pull request against `main` and fill in the PR template.

## Coding conventions

- **Formatting:** all code must be `gofmt`-clean. CI enforces this.
- **Linting:** code must pass `golangci-lint` (config in [`.golangci.yml`](.golangci.yml)).
- **Errors:** wrap errors with context using `fmt.Errorf("...: %w", err)`. Don't
  silently ignore errors except for the documented best-effort calls in the
  errcheck allowlist.
- **License header:** every `.go` file must start with the Apache 2.0 header
  (copy it from any existing source file).
- **Tests:** unit tests live next to the code (`*_test.go`); integration tests
  use the `//go:build integration` tag and the helpers in `internal/testutil`.

## Commit messages

Write clear, imperative commit subjects (e.g. "Add quantity search support").
Reference issues in the body where applicable (`Resolves #123`).

## Reporting bugs / requesting features

Open an issue using the issue template. For security vulnerabilities, please
follow [SECURITY.md](SECURITY.md) instead of filing a public issue.

## License

By contributing, you agree that your contributions will be licensed under the
[Apache License 2.0](LICENSE).
