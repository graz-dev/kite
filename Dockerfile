# Build stage
FROM golang:1.23-alpine AS builder

ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=dev

WORKDIR /workspace

# Cache dependencies first
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY api/       api/
COPY cmd/       cmd/
COPY internal/  internal/

# Build the binary
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -a -o kite-operator ./cmd/main.go

# ─────────────────────────────────────────────────────────────────────────────
# Runtime stage – distroless for minimal attack surface
FROM gcr.io/distroless/static:nonroot

WORKDIR /
COPY --from=builder /workspace/kite-operator .

# Run as non-root
USER 65532:65532

ENTRYPOINT ["/kite-operator"]
