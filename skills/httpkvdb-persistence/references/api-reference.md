# HTTPKVDB API Reference

Use this reference when writing clients, examples, tests, or integration code against this repository's actual HTTP API.

## Service Basics

Default API prefixes:

```text
/v1
/api/v1
```

Unauthenticated health endpoints:

```http
GET /healthz
GET /readyz
GET /metrics
```

Protected endpoints require authentication middleware. APIKey authentication is accepted with:

```http
APIKey: <api-key>
```

JWT authentication uses the usual bearer form:

```http
Authorization: Bearer <jwt>
```

Do not log API keys, JWTs, authorization headers, or raw stored values.

## Userspace Rules

Every authenticated principal resolves to one `userspace_id`. Ordinary KV APIs use the authenticated userspace. Clients cannot choose another userspace by path.

The path form below checks that the URL userspace matches the authenticated principal:

```text
/api/v1/{userspace_id}/{key}
```

The compatibility form below uses only the authenticated userspace:

```text
/v1/kv/{key}
```

Admin users can operate across userspaces through admin endpoints.

## CRUD

Write or overwrite a key:

```bash
curl -i -X PUT "$BASE/api/v1/$USERSPACE/app%2Fv1%2Fusers%2Fu_1" \
  -H "APIKey: $API_KEY" \
  -H "Content-Type: application/json" \
  --data '{"id":"u_1","email":"alice@example.com","name":"Alice"}'
```

Response:

```http
HTTP/1.1 200 OK
X-KV-Version: <version>
```

Read a key:

```bash
curl -i "$BASE/api/v1/$USERSPACE/app%2Fv1%2Fusers%2Fu_1" \
  -H "APIKey: $API_KEY"
```

Response includes:

```http
Content-Type: application/json
X-KV-Version: <version>
X-KV-Size: <bytes>
X-KV-Checksum: sha256:<checksum>
```

Read metadata only:

```bash
curl -I "$BASE/api/v1/$USERSPACE/app%2Fv1%2Fusers%2Fu_1" \
  -H "APIKey: $API_KEY"
```

Delete:

```bash
curl -i -X DELETE "$BASE/api/v1/$USERSPACE/app%2Fv1%2Fusers%2Fu_1" \
  -H "APIKey: $API_KEY"
```

Default delete behavior is strict: missing keys return `404 KEY_NOT_FOUND`.

## Value Types

HTTPKVDB stores bytes and metadata. `Content-Type` determines value type:

```text
text/plain                  -> string
application/json            -> json, validated by server
application/octet-stream    -> binary
other                       -> binary with original Content-Type preserved
```

Use `application/json` for structured documents and tests that validate invalid JSON returns `422 INVALID_JSON`.

## Transactions

Create a transaction:

```bash
curl -s -X POST "$BASE/v1/tx" \
  -H "APIKey: $API_KEY" \
  -H "Content-Type: application/json" \
  --data '{"total_ops":2,"timeout_ms":30000}'
```

Add operation fragment `seq=1`:

```bash
curl -i -X POST "$BASE/v1/tx/$TX_ID/ops/1" \
  -H "APIKey: $API_KEY" \
  -H "X-KV-Op: PUT" \
  -H "X-KV-Key: app%2Fv1%2Fusers%2Fu_1" \
  -H "X-KV-Op-Id: op-create-user" \
  -H "Content-Type: application/json" \
  --data '{"id":"u_1","email":"alice@example.com","name":"Alice"}'
```

Add operation fragment `seq=2`:

```bash
curl -i -X POST "$BASE/v1/tx/$TX_ID/ops/2" \
  -H "APIKey: $API_KEY" \
  -H "X-KV-Op: PUT" \
  -H "X-KV-Key: app%2Fv1%2Findex%2Fusers%2Femail%2Falice%2540example.com" \
  -H "X-KV-Op-Id: op-create-email-index" \
  -H "Content-Type: application/json" \
  --data '{"user_id":"u_1"}'
```

Commit:

```bash
curl -i -X POST "$BASE/v1/tx/$TX_ID/commit" \
  -H "APIKey: $API_KEY" \
  -H "Content-Type: application/json" \
  --data '{"total_ops":2}'
```

Query result:

```bash
curl -i "$BASE/v1/tx/$TX_ID/result" -H "APIKey: $API_KEY"
```

Abort:

```bash
curl -i -X POST "$BASE/v1/tx/$TX_ID/abort" -H "APIKey: $API_KEY"
```

Supported transaction operations:

```text
GET
PUT
DELETE
EXISTS
HEAD
```

Rules:

- `seq` starts at 1 and must be within `total_ops`.
- `X-KV-Op-Id` is required for idempotent retry.
- Duplicate `tx_id + seq` with identical content is accepted.
- Duplicate `tx_id + seq` with different content aborts with `409 SEQ_CONFLICT`.
- `PUT` must include a body; non-`PUT` operations must not include a body.
- GET results in transaction responses use base64 value encoding.

## Admin APIs

Admin role is required.

List userspaces:

```bash
curl -i "$BASE/v1/admin/userspaces" -H "APIKey: $ADMIN_API_KEY"
```

Create userspace:

```bash
curl -i -X POST "$BASE/v1/admin/userspaces" \
  -H "APIKey: $ADMIN_API_KEY" \
  -H "Content-Type: application/json" \
  --data '{"userspace_id":"alice","user_id":"alice"}'
```

The generated `api_key` is returned only once.

List keys in a userspace:

```bash
curl -i "$BASE/v1/admin/userspaces/alice/keys" \
  -H "APIKey: $ADMIN_API_KEY"
```

Admin KV access:

```text
/v1/admin/userspaces/{userspace_id}/kv/{key}
```

Rotate userspace API key:

```bash
curl -i -X POST "$BASE/v1/admin/userspaces/alice/api-key" \
  -H "APIKey: $ADMIN_API_KEY"
```

Delete userspace:

```bash
curl -i -X DELETE "$BASE/v1/admin/userspaces/alice" \
  -H "APIKey: $ADMIN_API_KEY"
```

## Import And Export

Export current authenticated userspace:

```bash
curl -o kv-export.bin "$BASE/v1/export" -H "APIKey: $API_KEY"
```

Import into current authenticated userspace:

```bash
curl -i -X POST "$BASE/v1/import" \
  -H "APIKey: $API_KEY" \
  -H "X-KV-Import-Mode: merge-overwrite" \
  --data-binary @kv-export.bin
```

Import modes:

```text
replace
merge-overwrite
merge-skip
```

Import/export goes through the global serializable lock. The wire format is binary and begins with magic `KVHTTP01`.
