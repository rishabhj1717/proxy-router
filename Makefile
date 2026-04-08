.PHONY: run build tidy docker-build docker-run test clean deps

# Local data directory for SQLite (overrides the /data default)
DATA_DIR ?= $(PWD)/.data
DB_PATH  ?= $(DATA_DIR)/alb.db

# ── Dependencies ─────────────────────────────────────────────────────────────
deps:
	@echo "==> Downloading Go modules..."
	go mod download
	@echo "==> Tidying go.sum..."
	go mod tidy

# ── Run locally ──────────────────────────────────────────────────────────────
run: deps
	@mkdir -p $(DATA_DIR)
	ALB_LISTEN_ADDR=:8080 \
	ALB_ADMIN_ADDR=:9090 \
	ALB_DB_PATH=$(DB_PATH) \
	ALB_DIAL_TIMEOUT_SEC=5 \
	ALB_RESPONSE_TIMEOUT_SEC=30 \
	go run ./cmd/alb

# ── Build binary ─────────────────────────────────────────────────────────────
build: deps
	@echo "==> Building binary..."
	CGO_ENABLED=1 go build -ldflags="-w -s" -o ./bin/alb ./cmd/alb
	@echo "==> Binary at ./bin/alb"

# ── Run compiled binary ───────────────────────────────────────────────────────
run-bin: build
	@mkdir -p $(DATA_DIR)
	ALB_LISTEN_ADDR=:8080 \
	ALB_ADMIN_ADDR=:9090 \
	ALB_DB_PATH=$(DB_PATH) \
	./bin/alb

# ── Docker ───────────────────────────────────────────────────────────────────
docker-build:
	podman build --platform=linux/amd64 -t alb:local .

docker-run: docker-build
	@mkdir -p $(DATA_DIR)
	docker run --rm \
		-p 8080:8080 \
		-p 9090:9090 \
		-v $(DATA_DIR):/data \
		-e ALB_DB_PATH=/data/alb.db \
		alb:local

# ── Smoke test (requires running ALB) ────────────────────────────────────────
smoke-test:
	@echo "==> Health check..."
	curl -sf http://localhost:9090/healthz | jq .

	@echo "\n==> Adding a test route (points to httpbin.org)..."
	curl -sf -X POST http://localhost:9090/routes \
		-H "Content-Type: application/json" \
		-d '{"sandbox_id":"test-sb","pattern":"^/test(/.*)?$$","target_url":"https://httpbin.org","priority":10}' | jq .

	@echo "\n==> Listing routes..."
	curl -sf http://localhost:9090/routes | jq .

	@echo "\n==> Proxying a request through the data plane..."
	curl -sv http://localhost:8080/test/get 2>&1 | grep -E "< HTTP|X-ALB|x-alb"

	@echo "\n==> Testing 404 on unmatched path..."
	curl -sf http://localhost:8080/no-such-path || true

# ── Clean ─────────────────────────────────────────────────────────────────────
clean:
	rm -rf ./bin $(DATA_DIR)

tidy:
	go mod tidy
