# Build on the native builder arch and cross-compile to the target platform via
# GOOS/GOARCH — far faster than emulating the whole toolchain under QEMU when
# building multi-arch images with buildx.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Set by buildx for the target platform; TARGETARCH is empty for a plain
# `docker build` without buildkit, in which case go builds for the native arch.
ARG TARGETOS
ARG TARGETARCH
# Release version stamped into the binary, matching the release workflow's
# ldflags. Left empty by default so the version package falls back to the
# embedded internal/version/VERSION file (a plain `docker build` stays correct).
ARG VERSION=
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags="-s -w -X github.com/wso2/fhir-server/internal/version.Version=${VERSION}" \
      -o fhir-server ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /app/fhir-server /fhir-server
EXPOSE 9090
USER nonroot:nonroot
ENTRYPOINT ["/fhir-server"]
