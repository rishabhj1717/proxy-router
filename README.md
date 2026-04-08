# ALB — In-House Application Load Balancer

A dynamic, regex-based reverse proxy for Kubernetes sandbox environments.
Written in Go, backed by SQLite (WAL mode), with a dual-port architecture:
- **:8080** — Data plane (proxies traffic to backends)
- **:9090** — Admin API (manage routes dynamically)

---

## Prerequisites

| Tool | Min Version | Install |
|------|-------------|---------|
| Go   | 1.22        | https://go.dev/dl |
| gcc  | any         | `apt install gcc` / `brew install gcc` |
| make | any         | usually pre-installed |
| curl + jq | any  | `apt install curl jq` / `brew install curl jq` |
| Docker | 20+      | https://docs.docker.com/get-docker (optional) |

> **Why gcc?** The `go-sqlite3` driver wraps the C SQLite amalgamation via CGO.
> CGO_ENABLED=1 and a C compiler are required at build time.

---

## Quick Start (local, no Docker)

```bash
# 1. Unzip the project
Clone the project
cd proxy-router

# 2. Download Go dependencies
go mod download

# 3. Run the ALB (creates .data/alb.db automatically)
make run
```

You should see output like:
```
{"level":"info","msg":"starting ALB","listen":":8080","admin":":9090","db":".data/alb.db"}
{"level":"info","msg":"routing engine initialised","routes_loaded":0}
{"level":"info","msg":"data plane listening","addr":":8080"}
{"level":"info","msg":"admin plane listening","addr":":9090"}
```

---

## Step-by-Step Local Setup

### Step 1 — Verify prerequisites

```bash
go version          # must be go1.22+
gcc --version       # any version
make --version
curl --version
```

### Step 2 — Install Go modules

```bash
go mod download
go mod tidy
```

### Step 3 — Create the data directory

The ALB stores its SQLite DB here. Override with `ALB_DB_PATH` env var if needed.

```bash
mkdir -p .data
```

### Step 4 — Run with `go run` (fastest for dev)

```bash
ALB_LISTEN_ADDR=:8080 \
ALB_ADMIN_ADDR=:9090 \
ALB_DB_PATH=.data/alb.db \
go run ./cmd/alb
```

Or simply:
```bash
make run
```

### Step 5 — (Optional) Build a binary

```bash
make build
# Binary placed at ./bin/alb

make run-bin   # build + run the compiled binary
```

---

## Testing the ALB

Open a **second terminal** and run these commands while the ALB is running.

### Health check

```bash
curl http://localhost:9090/healthz
# {"status":"ok"}
```

### Add a route

Routes are regex patterns mapped to backend URLs.

```bash
# Route /api/users/* → a local backend
curl -X POST http://localhost:9090/routes \
  -H "Content-Type: application/json" \
  -d '{
    "sandbox_id": "sandbox-1",
    "pattern":    "^/api/users(/.*)?$",
    "target_url": "http://httpbin.org",
    "priority":   10
  }'

curl -X POST http://localhost:9090/routes \
  -H "Content-Type: application/json" \
  -d '{
    "sandbox_id": "sandbox-tmp2",
    "pattern":    "^/websdk/k8s-app-v3/.*",
    "target_url": "http://dev-eks-svc-websdk.hs-pre-issueflow.svc.cluster.local:9017",
    "priority":   1
  }'
# {"id":1}
```
BASE=https://api-router.sbox.dev.helpshift.com/websdk/k8s-app-v3/test

### List all routes

```bash
curl http://localhost:9090/routes | jq .
```

### Test proxy (data plane)

```bash
# Should proxy to httpbin.org/get and return 200
curl -v http://localhost:8080/api/users/get

# Check for ALB response headers:
# X-ALB-Sandbox: sandbox-1
# X-ALB-Route-ID: 1
```

### Test 404 — no matching route

```bash
curl -v http://localhost:8080/no-such-path
# HTTP/1.1 404 Not Found
# {"error":"no route matched","path":"/no-such-path"}
```

### Test 502 — unreachable backend

```bash
# Add a route pointing to a dead service
curl -X POST http://localhost:9090/routes \
  -H "Content-Type: application/json" \
  -d '{
    "sandbox_id": "sandbox-bad",
    "pattern":    "^/dead(/.*)?$",
    "target_url": "http://localhost:19999",
    "priority":   10
  }'

curl -v http://localhost:8080/dead/anything
# HTTP/1.1 502 Bad Gateway
# {"error":"bad gateway","target":"localhost:19999","detail":"..."}
```

### Delete a route

```bash
curl -X DELETE http://localhost:9090/routes/1
# {"status":"deleted"}
```

### Delete all routes for a sandbox

```bash
curl -X DELETE http://localhost:9090/sandboxes/sandbox-1
# {"deleted":2,"sandbox_id":"sandbox-1"}
```

### Force reload from DB

```bash
curl -X POST http://localhost:9090/routes/reload
# {"status":"reloaded"}
```

---

## Run the full smoke test

Requires the ALB to be running in another terminal.

```bash
make smoke-test
```

---

## Docker

### Build image

```bash
make docker-build
```

### Run container

```bash
make docker-run
# Binds :8080 and :9090, mounts .data/ as /data inside container
```

### Manual docker run

```bash
mkdir -p .data
docker run --rm \
  -p 8080:8080 \
  -p 9090:9090 \
  -v $(pwd)/.data:/data \
  -e ALB_DB_PATH=/data/alb.db \
  alb:local
```

---

## Kubernetes Deployment

### Prerequisites

- A running cluster (`kubectl cluster-info` should work)
- `kubectl` configured with appropriate permissions
- A container registry to push the image

### Steps

```bash
# 1. Build and push the image
docker build -t yourrepo/alb:latest .
docker push yourrepo/alb:latest

# 2. Update the image name in k8s/deployment.yaml
sed -i 's|yourrepo/alb:latest|<YOUR_REGISTRY>/alb:latest|g' k8s/deployment.yaml

# 3. Apply RBAC
kubectl apply -f k8s/rbac.yaml

# 4. Deploy (creates namespace, PVC, Deployment, Services)
kubectl apply -f k8s/deployment.yaml

# 5. Wait for rollout
kubectl rollout status deployment/alb -n sandbox-alb-system

# 6. Access the admin API via port-forward
kubectl port-forward svc/alb-admin 9090:9090 -n sandbox-alb-system &

# 7. Access the data plane via port-forward
kubectl port-forward svc/alb 8080:80 -n sandbox-alb-system &

# 8. Health check
curl http://localhost:9090/healthz
```

---

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `ALB_LISTEN_ADDR` | `:8080` | Data plane bind address |
| `ALB_ADMIN_ADDR` | `:9090` | Admin API bind address |
| `ALB_DB_PATH` | `/data/alb.db` | Path to SQLite database file |
| `ALB_DIAL_TIMEOUT_SEC` | `5` | TCP dial timeout to backends (seconds) |
| `ALB_RESPONSE_TIMEOUT_SEC` | `30` | Full response timeout (seconds) |

---

## Admin API Reference

| Method | Path | Description |
|--------|------|-------------|
| GET | `/healthz` | Liveness check |
| GET | `/routes` | List all active routes |
| POST | `/routes` | Add a new route |
| DELETE | `/routes/{id}` | Delete route by ID |
| POST | `/routes/reload` | Reload all routes from DB |
| DELETE | `/sandboxes/{id}` | Delete all routes for a sandbox |

### POST /routes — Request body

```json
{
  "sandbox_id": "sandbox-1",
  "pattern":    "^/api/v1/users(/.*)?$",
  "target_url": "http://user-service.sandbox-1.svc.cluster.local:8080",
  "priority":   10
}
```

- `priority`: lower number = evaluated first. Defaults to `100`.
- `pattern`: valid Go `regexp` syntax (RE2).

---

## Troubleshooting

**`cgo: C compiler "gcc" not found`**
→ Install gcc: `sudo apt install gcc` (Linux) or `xcode-select --install` (macOS)

**`cannot find package "github.com/mattn/go-sqlite3"`**
→ Run `go mod download` first

**`bind: address already in use`**
→ Change ports: `ALB_LISTEN_ADDR=:18080 ALB_ADMIN_ADDR=:19090 make run`

**SQLite "database is locked"**
→ Only one process should write to the DB. Stop any other ALB instance first.

**Docker build fails on arm64 (Apple Silicon)**
→ Add `--platform linux/amd64` or change `GOARCH` to `arm64` in the Dockerfile.
