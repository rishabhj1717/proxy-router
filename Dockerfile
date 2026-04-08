# ── Stage 1: Build ──────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

# CGO is required by go-sqlite3 (it wraps the C SQLite amalgamation).
RUN apk add --no-cache gcc musl-dev

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod tidy && go mod download

COPY . .

# BuildKit-provided args (also supported by Podman builds).
ARG TARGETOS=linux
ARG TARGETARCH

# Build a statically linked binary.
RUN set -eu; \
    GOOS="${TARGETOS:-$(go env GOOS)}"; \
    GOARCH="${TARGETARCH:-$(go env GOARCH)}"; \
    CGO_ENABLED=1 GOOS="$GOOS" GOARCH="$GOARCH" go build \
    -ldflags="-w -s -extldflags '-static'" \
    -o /alb \
    ./cmd/alb

# ── Stage 2: Runtime ────────────────────────────────────────────────────────
FROM golang:1.22-alpine

COPY --from=builder /alb /alb

# Data plane port
EXPOSE 8080
# Admin API port
EXPOSE 9090

ENTRYPOINT ["/alb"]
