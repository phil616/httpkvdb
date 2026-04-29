# Operations Guide

This guide summarizes how to build, configure, start, test, and deploy `httpkvdb`.

## Build

```bash
go test ./...
mkdir -p bin
go build -trimpath -ldflags='-s -w' -o bin/kvhttpd ./cmd/kvhttpd
```

## Configure

Use environment variables. Start from the template:

```bash
cp configs/kvhttpd.env.example .env.local
```

Edit these values first:

```bash
KVHTTP_ADDR=127.0.0.1:8080
KVHTTP_STORAGE_PATH=./data
KVHTTP_CORS_ALLOWED_ORIGINS=http://127.0.0.1:5173,http://localhost:5173
KVHTTP_BOOTSTRAP_API_KEY=<long-local-secret>
KVHTTP_JWT_SECRET=<long-jwt-secret>
```

Start with the file:

```bash
./bin/kvhttpd --config .env.local
```

If `--config` is omitted, the process reads configuration from environment variables.

## Start

```bash
./bin/kvhttpd --config .env.local
```

Verify:

```bash
curl -i http://127.0.0.1:8080/healthz
curl -i http://127.0.0.1:8080/readyz
curl -i http://127.0.0.1:8080/metrics
```

## Test

Run Go tests:

```bash
go test ./...
```

Run production-style HTTP tests with the built binary:

```bash
uv run python scripts/production_test.py --binary ./bin/kvhttpd --port 18080
```

Expected summary:

```json
{
  "passed": 11,
  "failed": 0,
  "total": 11
}
```

## Deploy

Recommended minimal production layout:

```text
/usr/local/bin/kvhttpd
/etc/httpkvdb/kvhttpd.env
/var/lib/httpkvdb/
```

Set permissions:

```bash
sudo chmod 600 /etc/httpkvdb/kvhttpd.env
sudo chmod 700 /var/lib/httpkvdb
```

For systemd deployment, use the service unit example in [README.md](../README.md).
