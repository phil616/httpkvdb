# HTTPKVDB Agent Integration Guide

This file is the client contract for AI agents that need to use an HTTPKVDB instance after a human provides:

- `BASE_URL`: HTTPKVDB service origin, for example `https://kvdb-api.example.com`
- `APP_NAME`: the third-party application name, used as the HTTPKVDB userspace, for example `webapp`
- `API_KEY`: API key plaintext

Treat `API_KEY`, JWTs, `Authorization`, request bodies, and response values as secrets unless the task explicitly says otherwise. Do not log them.

## Application Userspace Contract

Every third-party application must use its own application name as its HTTPKVDB userspace.

Example:

- application name: `webapp`
- userspace: `webapp`
- KV path prefix: `/api/v1/webapp/`

The HTTPKVDB administrator creates that userspace and gives the application operator:

- the service address, such as `https://kvdb-api.example.com`
- the APIKey generated for that userspace

The application must then perform all ordinary KV operations inside its dedicated userspace:

```text
https://kvdb-api.example.com/api/v1/webapp/{url-encoded-key}
```

Do not use another application name as the userspace. Do not share one userspace across unrelated applications. The server rejects a URL userspace that does not match the APIKey's authenticated principal with `403 FORBIDDEN`.

## Connection Model

HTTPKVDB is a single-node strongly consistent KV database exposed over HTTP. Authenticated endpoints are under `/v1` and `/api/v1`. Each credential resolves server-side to exactly one `userspace`; `/api/v1/{userspace}/{key}` is allowed only when the URL userspace matches the authenticated principal.

Use HTTPS when available. For every authenticated request, send one of:

```http
Authorization: ApiKey <API_KEY>
Authorization: Bearer <JWT>
APIKey: <API_KEY>
X-API-Key: <API_KEY>
```

When a human gives an API key, prefer `APIKey: <API_KEY>` for the userspace URL API. `Authorization: ApiKey <API_KEY>` remains supported for all authenticated APIs.

Optional request correlation:

```http
X-Request-Id: <client-generated-id>
```

Unauthenticated probes:

```http
GET /healthz
GET /readyz
GET /metrics
```

`/healthz` and `/readyz` return `200` with `ok\n` when healthy/ready.

## Key Encoding

Keys are strings and are placed in the URL path:

```text
/v1/kv/{url-encoded-key}
/api/v1/{userspace}/{url-encoded-key}
```

Always URL-encode the full key as one path segment. Encode `/` as `%2F`; do not let HTTP client path normalization split the key.

Examples:

- key `profile` -> `/v1/kv/profile`
- key `agents/session/42` -> `/v1/kv/agents%2Fsession%2F42`
- key `a b` -> `/v1/kv/a%20b`
- app/userspace `webapp`, key `profile` -> `/api/v1/webapp/profile`

## Value Types

HTTPKVDB stores opaque bytes plus `Content-Type`.

Supported client conventions:

- `text/plain` for strings
- `application/json` for JSON values; invalid JSON is rejected with `422 INVALID_JSON`
- `application/octet-stream` for binary values

If `Content-Type` is omitted on write, the server stores `application/octet-stream`.

## Ordinary KV Operations

Ordinary `PUT`, `GET`, `HEAD`, and `DELETE` are single-operation serializable transactions.

### Put

```http
PUT /v1/kv/{key}
APIKey: <API_KEY>
Content-Type: application/json

{"state":"ready"}
```

Success:

```http
HTTP/1.1 200 OK
X-KV-Version: <uint64>
```

The response body is empty. Existing keys are overwritten.

### Get

```http
GET /v1/kv/{key}
APIKey: <API_KEY>
```

Success:

```http
HTTP/1.1 200 OK
Content-Type: <stored-content-type>
X-KV-Version: <uint64>
X-KV-Size: <bytes>
X-KV-Checksum: <checksum>

<raw value bytes>
```

Not found:

```json
{"error":"KEY_NOT_FOUND","message":"key not found","request_id":"..."}
```

### Head

```http
HEAD /v1/kv/{key}
APIKey: <API_KEY>
```

Same metadata headers as `GET`, no body.

### Delete

```http
DELETE /v1/kv/{key}
APIKey: <API_KEY>
```

Success:

```http
HTTP/1.1 204 No Content
```

Deleting a missing key returns `404 KEY_NOT_FOUND`.

### Application Userspace URL API

Third-party applications should use the userspace URL API. `{userspace}` must be the application name assigned by the administrator:

```http
PUT /api/v1/{userspace}/{key}
GET /api/v1/{userspace}/{key}
HEAD /api/v1/{userspace}/{key}
DELETE /api/v1/{userspace}/{key}
APIKey: <API_KEY>
```

For example, an application named `webapp` stores its `profile` key at:

```http
PUT /api/v1/webapp/profile
APIKey: <WEBAPP_API_KEY>
Content-Type: application/json

{"enabled":true}
```

The server rejects mismatches between `{userspace}` and the authenticated principal with `403 FORBIDDEN`.

## Admin Userspaces

Only principals with the `admin` role may manage userspaces and their KV data.

```http
POST /v1/admin/userspaces
APIKey: <ADMIN_API_KEY>
Content-Type: application/json

{"userspace_id":"webapp","user_id":"webapp"}
```

Success:

```json
{"user_id":"webapp","userspace_id":"webapp","api_key":"..."}
```

The generated `api_key` is plaintext and returned only in this response. The server stores only its HMAC-SHA256 digest. Re-creating an existing userspace returns `409 USERSPACE_EXISTS`.

Management endpoints:

```http
GET    /v1/admin/userspaces
DELETE /v1/admin/userspaces/{userspace}
POST   /v1/admin/userspaces/{userspace}/api-key
GET    /v1/admin/userspaces/{userspace}/keys
PUT    /v1/admin/userspaces/{userspace}/kv/{key}
GET    /v1/admin/userspaces/{userspace}/kv/{key}
HEAD   /v1/admin/userspaces/{userspace}/kv/{key}
DELETE /v1/admin/userspaces/{userspace}/kv/{key}
APIKey: <ADMIN_API_KEY>
```

`POST /v1/admin/userspaces/{userspace}/api-key` replaces that userspace's APIKey and returns the new plaintext key once. Admin KV operations are for granular operational management and still pass through the global serializable lock.

## Multi-Request Transactions

Use transaction APIs when multiple KV operations must commit atomically and serializably. Transaction fragments are only recorded before commit; they are not executed and are not visible until commit succeeds.

Flow:

1. `POST /v1/tx` creates a transaction with fixed `total_ops`.
2. `POST /v1/tx/{tx_id}/ops/{seq}` records each operation fragment. `seq` is 1-based.
3. `POST /v1/tx/{tx_id}/commit` commits once all fragments are present.
4. `GET /v1/tx/{tx_id}/result` reads committed result or current status.

### Create Transaction

```http
POST /v1/tx
Authorization: ApiKey <API_KEY>
Content-Type: application/json

{
  "tx_id": "optional-client-id",
  "total_ops": 3,
  "timeout_ms": 30000
}
```

`tx_id` may be omitted; generated IDs look like `tx_<hex>`. If supplied, it must match `[A-Za-z0-9_.:-]{1,128}`.

Success:

```json
{
  "tx_id": "tx_...",
  "status": "pending",
  "total_ops": 3,
  "deadline": "2026-05-07T05:00:00Z"
}
```

### Add Operation

```http
POST /v1/tx/{tx_id}/ops/{seq}
Authorization: ApiKey <API_KEY>
X-KV-Op: PUT
X-KV-Key: profile
X-KV-Op-Id: op-put-profile
Content-Type: application/json

{"name":"agent"}
```

Operation headers:

- `X-KV-Op`: one of `GET`, `PUT`, `DELETE`, `EXISTS`, `HEAD`
- `X-KV-Key`: URL-encoded or raw key string; use URL encoding for ambiguous characters
- `X-KV-Op-Id`: stable client operation ID, required
- `Content-Type`: required only to preserve value type for `PUT`

`PUT` must include a non-empty body. `GET`, `DELETE`, `EXISTS`, and `HEAD` must not include a body.

Success before commit:

```json
{
  "tx_id": "tx_...",
  "status": "pending",
  "total_ops": 3,
  "received_seq": [1],
  "missing_seq": [2,3]
}
```

Re-sending the same `tx_id` + `seq` with identical `X-KV-Op-Id`, op type, key, content type, and body hash is idempotent. Re-sending the same `seq` with different content aborts the transaction and returns `409 SEQ_CONFLICT`.

### Commit

```http
POST /v1/tx/{tx_id}/commit
Authorization: ApiKey <API_KEY>
Content-Type: application/json

{"total_ops":3}
```

If all fragments are present, the response is a committed result:

```json
{
  "tx_id": "tx_...",
  "status": "committed",
  "results": [
    {"seq":1,"op":"PUT","status":200,"key":"profile","version":1},
    {"seq":2,"op":"GET","status":200,"key":"profile","content_type":"application/json","value_base64":"eyJuYW1lIjoiYWdlbnQifQ==","version":1},
    {"seq":3,"op":"DELETE","status":204,"key":"profile","version":2}
  ]
}
```

Transaction `GET` result values are Base64 in `value_base64`; decode them before use.

If commit arrives before all fragments:

```json
{
  "tx_id": "tx_...",
  "status": "waiting_for_ops",
  "total_ops": 3,
  "received_seq": [1],
  "missing_seq": [2,3]
}
```

After the missing fragments arrive, the server may commit during the final add-op call. Check that response for `status: "committed"` or call the result endpoint.

Optional integrity check: commit body may include `tx_digest`. The digest format is `sha256:<hex>` over operation lines sorted by `seq`:

```text
<seq>\0<op_id>\0<op_type>\0<key>\0<body_hash>\n
```

`body_hash` is `sha256:<hex>` of the raw request body; for no-body ops it is the SHA-256 of empty bytes.

### Result

```http
GET /v1/tx/{tx_id}/result
Authorization: ApiKey <API_KEY>
```

Returns either status metadata or a committed/aborted `TxResult`.

### Abort

```http
POST /v1/tx/{tx_id}/abort
Authorization: ApiKey <API_KEY>
```

Success:

```json
{"tx_id":"tx_...","status":"aborted"}
```

## Export and Import

Export/import operate on the authenticated credential's userspace only.

### Export

```http
GET /v1/export
Authorization: ApiKey <API_KEY>
```

Success:

```http
HTTP/1.1 200 OK
Content-Type: application/octet-stream
Content-Disposition: attachment; filename="kv-export.bin"

<binary export payload>
```

The binary format is HTTPKVDB-specific. Treat it as opaque unless implementing a format-compatible importer.

### Import

```http
POST /v1/import
Authorization: ApiKey <API_KEY>
Content-Type: application/octet-stream
X-KV-Import-Mode: replace

<binary export payload>
```

Import modes:

- `replace`: clear current userspace, then import records
- `merge-overwrite`: merge and overwrite conflicting keys
- `merge-skip`: merge and keep existing conflicting keys

If `X-KV-Import-Mode` is omitted, mode is `replace`.

Success:

```json
{"imported":10,"skipped":0,"replaced":0}
```

## Error Contract

Errors are JSON:

```json
{
  "error": "KEY_NOT_FOUND",
  "message": "key not found",
  "request_id": "..."
}
```

Common status/error pairs:

- `400 BAD_REQUEST`: malformed request
- `400 INVALID_KEY`: invalid key
- `400 INVALID_USERSPACE`: invalid userspace identifier
- `400 INVALID_TX`: invalid transaction parameter or operation
- `401 UNAUTHORIZED`: missing/invalid credential
- `403 FORBIDDEN`: transaction belongs to another principal
- `404 KEY_NOT_FOUND`: key missing
- `404 TX_NOT_FOUND`: transaction missing
- `409 SEQ_CONFLICT`: same transaction sequence received different content
- `409 USERSPACE_EXISTS`: userspace already exists
- `409 TX_ALREADY_COMMITTED`: transaction already committed
- `409 TX_ABORTED`: transaction aborted
- `410 TX_EXPIRED`: transaction deadline passed
- `413 VALUE_TOO_LARGE`: body exceeds server limit
- `422 INVALID_JSON`: `application/json` body is not valid JSON
- `500 STORAGE_ERROR`: storage failure

## Minimal Agent Client Algorithm

Given `(BASE_URL, APP_NAME, API_KEY)`:

1. Normalize `BASE_URL` by removing trailing `/`.
2. Treat `APP_NAME` as the userspace.
3. For authenticated calls, set `APIKey: ${API_KEY}`.
4. Encode keys with path-segment percent encoding.
5. Use `/api/v1/${APP_NAME}/${encodedKey}` for ordinary KV operations.
6. Use transactions only when the action requires atomic multi-step read/write/delete semantics; transaction APIs still bind to the APIKey's userspace server-side.
7. On `401`, stop and ask for a valid credential.
8. On `403`, verify that `APP_NAME` exactly matches the userspace created by the administrator for this APIKey.
9. On `404 KEY_NOT_FOUND`, treat as absent key, not transport failure.
10. On `409` or `410` for transactions, create a new transaction unless the task requires preserving the original transaction ID.
11. Never include secrets or raw values in logs, trace summaries, error reports, or prompts unless explicitly authorized.

## Curl Smoke Test

```sh
BASE_URL="https://kvdb-api.example.com"
API_KEY="provided-by-human"
APP_NAME="webapp"
KEY="$(python3 -c 'import urllib.parse; print(urllib.parse.quote("agent/smoke", safe=""))')"

curl -fsS -X PUT "$BASE_URL/api/v1/$APP_NAME/$KEY" \
  -H "APIKey: $API_KEY" \
  -H "Content-Type: application/json" \
  --data '{"ok":true}'

curl -fsS "$BASE_URL/api/v1/$APP_NAME/$KEY" \
  -H "APIKey: $API_KEY"
```
