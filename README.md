# httpkvdb

`httpkvdb` is a single-node strongly consistent KV database exposed over HTTP. It supports per-user userspace isolation, APIKey and JWT authentication, JSON/string/binary values, multi-request transactions, and binary import/export.

The authoritative behavior specification is [docs/SPEC.md](docs/SPEC.md).

GitHub CI/release setup is documented in [docs/GITHUB.md](docs/GITHUB.md), and version synchronization rules are documented in [docs/VERSIONS.md](docs/VERSIONS.md).

## Status

This implementation prioritizes correctness over throughput:

- Ordinary `PUT` / `GET` / `HEAD` / `DELETE` requests are treated as single-operation serializable transactions.
- Ordinary CRUD, transaction commit, import, and export all pass through one global serializable lock.
- Transaction fragments are persisted but not executed until commit.
- Transaction commit executes operations by `seq` order inside one atomic storage update.
- API keys are stored as SHA-256 hashes only.
- Logs must not include API keys, `Authorization` headers, or raw values.

## Requirements

- Go 1.22+ source compatibility; Go 1.26.2 preferred toolchain from `go.mod`
- Python 3.11+ for production test scripts
- `uv` for running Python scripts in the local test flow

## Build

Run the normal test suite first:

```bash
go test ./...
```

Build the server binary:

```bash
mkdir -p bin
go build -trimpath -ldflags='-s -w' -o bin/kvhttpd ./cmd/kvhttpd
```

The resulting binary is:

```text
bin/kvhttpd
```

## Configuration

The server can be configured through an env-style config file or environment variables. A complete template is available at [configs/kvhttpd.env.example](configs/kvhttpd.env.example).

For local development:

```bash
cp configs/kvhttpd.env.example .env.local
```

Edit `.env.local`, then start with an explicit config file:

```bash
./bin/kvhttpd --config .env.local
```

When `--config` is provided, the server reads configuration from that file and uses built-in defaults for missing keys. It does not read process environment variables for missing values. When `--config` is omitted, the server reads environment variables.

Important production notes:

- Change `KVHTTP_BOOTSTRAP_API_KEY`; the default is only for local development.
- Change `KVHTTP_API_KEY_PEPPER`; API keys are stored as HMAC-SHA256 digests using this server-side secret.
- Change `KVHTTP_JWT_SECRET`; the default is only for local development.
- Set `KVHTTP_CORS_ALLOWED_ORIGINS` to the exact frontend origins that should be allowed.
- Use a persistent directory for `KVHTTP_STORAGE_PATH`.
- Protect the environment file because it contains secret material.
- Do not put userspace IDs in client requests; userspace is derived from authentication.

## Start

Development start:

```bash
KVHTTP_ADDR=127.0.0.1:8080 \
KVHTTP_STORAGE_PATH=./data \
KVHTTP_CORS_ALLOWED_ORIGINS=http://127.0.0.1:5173,http://localhost:5173 \
KVHTTP_BOOTSTRAP_API_KEY=dev-secret-key \
KVHTTP_API_KEY_PEPPER=dev-api-key-pepper \
./bin/kvhttpd
```

Health checks:

```bash
curl -i http://127.0.0.1:8080/healthz
curl -i http://127.0.0.1:8080/readyz
```

Authenticated request example:

```bash
curl -i \
  -X PUT 'http://127.0.0.1:8080/v1/kv/profile' \
  -H 'Authorization: ApiKey dev-secret-key' \
  -H 'Content-Type: application/json' \
  --data '{"name":"Alice"}'

curl -i \
  'http://127.0.0.1:8080/v1/kv/profile' \
  -H 'Authorization: ApiKey dev-secret-key'
```

## Deployment

### Docker Compose Deployment

This repository includes a `Dockerfile` and `docker-compose.yml`. Compose builds the image, publishes port `8080`, and persists the database file in the Docker volume `httpkvdb_data`.

Create a local-only `.env` file first:

```bash
cat > .env <<'EOF'
KVHTTP_BOOTSTRAP_API_KEY=replace-with-a-long-random-secret
KVHTTP_API_KEY_PEPPER=replace-with-a-long-random-api-key-pepper
KVHTTP_JWT_SECRET=replace-with-a-long-random-jwt-secret
EOF
chmod 600 .env
```

You can generate random secret values with:

```bash
openssl rand -hex 32
```

Start the service:

```bash
docker compose up -d --build
```

Check the service:

```bash
docker compose ps
curl -i http://127.0.0.1:8080/healthz
curl -i http://127.0.0.1:8080/readyz
```

Authenticated write and read example:

```bash
curl -i \
  -X PUT 'http://127.0.0.1:8080/v1/kv/profile' \
  -H 'Authorization: ApiKey replace-with-a-long-random-secret' \
  -H 'Content-Type: application/json' \
  --data '{"name":"Alice"}'

curl -i \
  'http://127.0.0.1:8080/v1/kv/profile' \
  -H 'Authorization: ApiKey replace-with-a-long-random-secret'
```

Stop the service while keeping data:

```bash
docker compose down
```

Remove the service and persistent data:

```bash
docker compose down -v
```

Note: `httpkvdb` is a single-node database. Do not mount the same persistent directory into multiple container instances in production, and do not horizontally scale this service with Compose `--scale`.

### Binary Deployment

1. Build on the target host or in CI:

   ```bash
   go test ./...
   go build -trimpath -ldflags='-s -w' -o bin/kvhttpd ./cmd/kvhttpd
   ```

2. Install the binary:

   ```bash
   sudo install -m 0755 bin/kvhttpd /usr/local/bin/kvhttpd
   ```

3. Create directories:

   ```bash
   sudo mkdir -p /var/lib/httpkvdb /etc/httpkvdb
   sudo chmod 700 /var/lib/httpkvdb /etc/httpkvdb
   ```

4. Install and edit config:

   ```bash
   sudo cp configs/kvhttpd.env.example /etc/httpkvdb/kvhttpd.env
   sudo chmod 600 /etc/httpkvdb/kvhttpd.env
   sudo editor /etc/httpkvdb/kvhttpd.env
   ```

5. Start manually:

   ```bash
   /usr/local/bin/kvhttpd --config /etc/httpkvdb/kvhttpd.env
   ```

### systemd Example

Create `/etc/systemd/system/kvhttpd.service`:

```ini
[Unit]
Description=httpkvdb single-node HTTP KV database
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/kvhttpd --config /etc/httpkvdb/kvhttpd.env
Restart=on-failure
RestartSec=2s
User=kvhttpd
Group=kvhttpd
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ReadWritePaths=/var/lib/httpkvdb

[Install]
WantedBy=multi-user.target
```

Example service setup:

```bash
sudo useradd --system --home /var/lib/httpkvdb --shell /usr/sbin/nologin kvhttpd
sudo chown -R kvhttpd:kvhttpd /var/lib/httpkvdb
sudo systemctl daemon-reload
sudo systemctl enable --now kvhttpd
sudo systemctl status kvhttpd
```

## Testing

### Unit and Integration Tests

```bash
go test ./...
```

These tests cover storage persistence, userspace isolation, APIKey/JWT auth mapping, auth cache behavior, transaction ordering, idempotency, rollback, expiration, import/export checksum, and HTTP integration paths.

### Production-Style Functional Test

The production-style test uses the built binary, starts it as a real process, calls HTTP endpoints, restarts the process, and emits a JSON report.

Build first:

```bash
go build -trimpath -ldflags='-s -w' -o bin/kvhttpd ./cmd/kvhttpd
```

Run:

```bash
uv run python scripts/production_test.py --binary ./bin/kvhttpd --port 18080
```

The script verifies:

- health and readiness
- unauthenticated request rejection
- JSON KV CRUD and metadata headers
- invalid JSON rejection
- binary value round trip
- transaction fragments are not visible before commit
- out-of-order transaction fragments execute by `seq`
- duplicate commit returns the same committed result
- binary export/import modes
- metrics endpoint
- committed data survives process restart

The report intentionally does not print API keys, `Authorization` headers, or raw values.

To keep the temporary data directory for debugging:

```bash
uv run python scripts/production_test.py --binary ./bin/kvhttpd --port 18080 --keep-data
```

## HTTP API Quick Reference

All authenticated APIs use `/v1`.

Authentication:

```http
Authorization: ApiKey <api_key>
Authorization: Bearer <jwt>
```

KV:

```text
PUT    /v1/kv/{url-encoded-key}
GET    /v1/kv/{url-encoded-key}
HEAD   /v1/kv/{url-encoded-key}
DELETE /v1/kv/{url-encoded-key}
```

Transactions:

```text
POST /v1/tx
POST /v1/tx/{tx_id}/ops/{seq}
POST /v1/tx/{tx_id}/commit
GET  /v1/tx/{tx_id}/result
POST /v1/tx/{tx_id}/abort
```

Import/export:

```text
GET  /v1/export
POST /v1/import
```

Observability:

```text
GET /healthz
GET /readyz
GET /metrics
```

## Storage

The current storage backend writes one JSON snapshot file under `KVHTTP_STORAGE_PATH`:

```text
<storage-path>/httpkvdb.json
```

The file contains logically isolated sections for:

- user KV spaces
- system API key records
- system JWT subject records
- transaction state and committed results

Writes are persisted through a temporary file and atomic rename. Keep `KVHTTP_STORAGE_PATH` on durable local storage.

## Security Checklist

- Replace all default secrets before exposing the service.
- Restrict permissions on config and storage directories.
- Put TLS and network access control in front of the service if it is not bound to localhost.
- Do not log request bodies or `Authorization` headers.
- Rotate the bootstrap API key after creating real operational credentials when credential management APIs are added.
