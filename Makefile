# Makefile for wso2/fhir-server.
# Run `make help` for the list of targets.

BINARY      := fhir-server
PKG         := ./cmd/server
DOCKER_IMAGE := fhir-server:latest

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the server binary
	go build -o $(BINARY) $(PKG)

.PHONY: run
run: ## Run the server (uses env vars / --config)
	go run $(PKG)

.PHONY: test
test: ## Run unit tests with the race detector
	go test -race -count=1 ./...

.PHONY: test-integration
test-integration: ## Run integration tests (requires Docker)
	go test -tags integration -timeout 300s ./...

.PHONY: test-conformance
test-conformance: ## Run conformance tests (requires Docker)
	go test -tags conformance -timeout 300s ./internal/conformance/...

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: fmt
fmt: ## Format all Go sources
	gofmt -w .

.PHONY: lint
lint: ## Run golangci-lint (install: https://golangci-lint.run)
	golangci-lint run --build-tags=integration,conformance ./...

.PHONY: tidy
tidy: ## Tidy go.mod / go.sum
	go mod tidy

.PHONY: docker
docker: ## Build the Docker image
	docker build -t $(DOCKER_IMAGE) .

.PHONY: clean
clean: ## Remove build artifacts
	rm -f $(BINARY)
	go clean
