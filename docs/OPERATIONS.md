# Operations Guide

This guide summarizes how to build, configure, start, test, and deploy `httpkvdb`.

## Build

Build and test commands assume a source checkout. Release archives contain the
compiled server and Web assets, rather than the Go/TypeScript source tree.

```bash
go test ./...
mkdir -p bin
go build -trimpath -ldflags='-s -w' -o bin/kvhttpd ./cmd/kvhttpd
```

## Configure

Use an explicit env-style configuration file or process environment variables. Start from the template:

```bash
cp configs/kvhttpd.env.example .env.local
```

Edit these values first:

```bash
KVHTTP_ADDR=127.0.0.1:8080
KVHTTP_STORAGE_PATH=/var/lib/httpkvdb
KVHTTP_CORS_ALLOWED_ORIGINS=http://127.0.0.1:5173,http://localhost:5173
KVHTTP_BOOTSTRAP_API_KEY=<long-local-secret>
KVHTTP_API_KEY_PEPPER=<long-api-key-pepper>
KVHTTP_JWT_SECRET=<long-jwt-secret>
```

Start with the file:

```bash
./bin/kvhttpd --config .env.local
```

With `--config`, only that file is loaded; the process environment is not merged.
Without it, only `KVHTTP_` environment variables are loaded. Missing or empty
values use defaults. Unknown names, repeated file keys, malformed quoting,
invalid integers, nonpositive numeric limits, and overflowing durations fail startup.
The default transaction timeout must not exceed its configured maximum.

The file supports KEY=value, an optional `export ` prefix, whole-line comments,
single-quoted literal strings, and double-quoted Go escapes. It is not a shell
script: there is no variable/command expansion or inline-comment syntax.
All settings and defaults are listed in [SPEC section 19](SPEC.md#19-配置项).
Use a writable absolute data directory and create it with the service user's
ownership before starting. For local smoke tests, a private ./data directory is
also supported.

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

Run production-style HTTP tests with the built binary (Python 3.11+ standard library; uv is optional):

```bash
python3 scripts/production_test.py --binary ./bin/kvhttpd --port 18080
```

Expected summary:

```json
{
  "passed": 13,
  "failed": 0,
  "total": 13
}
```

## Deploy

Recommended minimal production layout:

```text
/usr/local/bin/kvhttpd
/etc/httpkvdb/kvhttpd.env
/var/lib/httpkvdb/
```

Runtime data is written under `KVHTTP_STORAGE_PATH`:

```text
/var/lib/httpkvdb/httpkvdb.db
```

The database holds an exclusive process lock. Stop the service before copying
`httpkvdb.db` for a full backup. The export API backs up only the authenticated
userspace's KV records, without authentication or transaction state.

On first upgrade, the legacy `httpkvdb.json` is migrated atomically; the JSON
and old `userspaces/` mirrors remain untouched and are no longer updated. Back
up the entire directory while stopped before upgrading. Do not downgrade using
the old snapshot after new writes. Invalid legacy files prevent startup.

Use an absolute `KVHTTP_STORAGE_PATH` in production. Relative paths resolve from
the process working directory. `KVHTTP_MAX_IMPORT_SIZE` limits the whole import
(default 1 GiB); `KVHTTP_MAX_VALUE_SIZE` still limits each value. Imports and
exports are buffered in memory, so choose limits appropriate to available RAM.

Set permissions:

```bash
sudo chmod 600 /etc/httpkvdb/kvhttpd.env
sudo chmod 700 /var/lib/httpkvdb
```

The Web console is deployed separately; kvhttpd does not serve web/dist. Configure
its backend URL and CORS as described in [web/README.md](../web/README.md).

For systemd deployment, use the service unit example in [README.zh.md](../README.zh.md#systemd-示例).
Explicitly set KVHTTP_STORAGE_PATH=/var/lib/httpkvdb in the configuration file;
setting only an environment variable has no effect when --config is used.

## Authentication and transaction maintenance

Use exactly one authentication header. JWT requires HS256, mapped sub, and valid
exp; there is no login/token-issuance API. Bootstrap runs only for initial setup.
Use the admin rotation endpoint to change a userspace APIKey; restarting with a
new bootstrap environment value does not rotate or revive credentials.

The cleaner expires pending transactions and prunes terminal operation bodies
after KVHTTP_TX_RESULT_RETENTION_MS (default one hour). Transaction identities,
hashes, and complete results are retained. This is not a transaction deletion or
file-compaction policy; result values can still consume space. The cumulative
body limit KVHTTP_MAX_TX_BODY_SIZE defaults to 256 MiB per transaction. The service
has no global active-transaction-count cap. /readyz checks schema/bucket read
accessibility, not durable write success, disk space, or cleaner health.

Metrics are process-local counters and reset on restart; supported counters and
transaction retry semantics are defined in [SPEC](SPEC.md#172-metrics). There are
no latency histograms, auth-cache metrics, or per-request audit logs.
