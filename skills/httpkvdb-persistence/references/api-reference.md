# HTTPKVDB API Reference

Use this reference to integrate with the implemented API. Inside the HTTPKVDB repository, consult `docs/SPEC.md` and `GUIDE.md` for the full contract and `configs/kvhttpd.env.example` for configuration. An installed copy of this skill may be used without those source files; confirm the deployed version when it differs.

## Authentication and Userspace

Set exactly one of `APIKey: <key>`, `X-API-Key: <key>`, `Authorization: ApiKey <key>`, or `Authorization: Bearer <jwt>`. Multiple sources or repeated authentication headers return 401. Authorization schemes are case-insensitive. Never log credentials, tokens, or values.

JWT supports HS256 only. It requires a mapped sub and valid exp, checks nbf when supplied, and checks iss/aud only when configured. There is no login, token issuance, or JWKS API; the userspace creation endpoint generates an APIKey, not a JWT mapping.

Each credential maps to a Principal/userspace. Ordinary CRUD uses that identity. `/api/v1/{userspace_id}/{key}` also requires the URL userspace to match, even for names such as `kv`, `tx`, or `admin`. Admin cross-userspace operations use separate admin routes. Userspace IDs match `[A-Za-z0-9][A-Za-z0-9._-]{0,127}`; an application name is just a convention.

Public endpoints: GET/HEAD `/healthz`, `/readyz`, `/metrics`. Readiness checks accessible schema and storage buckets, not a durable write or cleaner health. The Go server does not host the Web console.

## Keys and Values

Logical keys are non-empty UTF-8, default maximum 4096 bytes (configurable up to 32768). Percent-encode the complete key exactly once in URL paths and `X-KV-Key`. Do not form-encode spaces as plus signs.

| Logical key | Transport encoding |
|---|---|
| `app/v1/users/u_1` | `app%2Fv1%2Fusers%2Fu_1` |
| `a%2Fb` | `a%252Fb` |
| `100%` | `100%25` |
| `a b` | `a%20b` |

If modeling embeds an encoded email segment, that encoding is part of the logical key: logical `app/v1/index/users/email/alice%40example.com` becomes `app%2Fv1%2Findex%2Fusers%2Femail%2Falice%2540example.com` on the wire. The server does not infer hierarchical or filesystem semantics. Browsers may normalize `.`/`..` path keys; use transaction headers to address those keys.

Content-Type determines value_type: text/plain → string, application/json → validated JSON, all others → binary. Parameters are allowed; the original Content-Type is preserved. Omitted Content-Type defaults to application/octet-stream. Metadata is limited to 65535 bytes and excludes CR/LF/NUL. Invalid metadata returns 400; invalid JSON values return 422.

## CRUD

Both `/v1/kv/{encoded-key}` and `/api/v1/{userspace_id}/{encoded-key}` support:

| Method | Success | Missing key |
|---|---|---|
| PUT | 200, empty body, X-KV-Version; overwrites existing values | Creates key |
| GET | 200, raw bytes and metadata | 404 KEY_NOT_FOUND |
| HEAD | Same metadata, no body | 404, no body |
| DELETE | 204, no body | 404 KEY_NOT_FOUND |

Metadata: Content-Type, X-KV-Version, X-KV-Size (bytes), X-KV-Checksum (`sha256:<hex>`). Ordinary PUT accepts an empty value. KV methods outside this set return 405 with Allow. CRUD is globally serialized; separate requests are separate transactions.

```sh
curl -fsS -X PUT "$BASE/api/v1/$USERSPACE/app%2Fv1%2Fusers%2Fu_1" \
  -H "APIKey: $API_KEY" -H 'Content-Type: application/json' \
  --data '{"id":"u_1","name":"Alice"}'
```

Use private output capture for GET/export when data is sensitive.

## Transactions

| Request | Success |
|---|---|
| POST `/v1/tx` | 201, tx_id/status/total_ops/deadline |
| POST `/v1/tx/{tx_id}/ops/{seq}` | 202, current status or committed result |
| POST `/v1/tx/{tx_id}/commit` | 200 result; 202 waiting_for_ops |
| GET `/v1/tx/{tx_id}/result` | 200 status or result, including aborted/expired |
| POST `/v1/tx/{tx_id}/abort` | 200 aborted; committed → 409; expired → 410 |

Create with `{"tx_id":"optional-stable-id","total_ops":2,"timeout_ms":30000}`. IDs match `[A-Za-z0-9_.:-]{1,128}`; encode them in paths. Zero/omitted timeout uses the default; negative/over-maximum is invalid. Same ID/owner/total_ops creation is idempotent, still returning 201. Identity is bound to both user_id and userspace_id.

A fragment needs X-KV-Op, X-KV-Key (encoded once), and non-empty stable X-KV-Op-Id. Operations are PUT/GET/HEAD/DELETE/EXISTS; seq is 1..total_ops. PUT requires a non-empty body. Other operations reject any body, including unknown-length/chunked bodies. Fragments do not execute before commit.

```sh
curl -fsS -X POST "$BASE/v1/tx" -H "APIKey: $API_KEY" \
  -H 'Content-Type: application/json' \
  --data '{"tx_id":"client-create-1","total_ops":2}'

curl -fsS -X POST "$BASE/v1/tx/client-create-1/ops/1" \
  -H "APIKey: $API_KEY" -H 'X-KV-Op: PUT' \
  -H 'X-KV-Key: app%2Fv1%2Fusers%2Fu_1' -H 'X-KV-Op-Id: put-user' \
  -H 'Content-Type: application/json' --data '{"id":"u_1"}'

curl -fsS -X POST "$BASE/v1/tx/client-create-1/ops/2" \
  -H "APIKey: $API_KEY" -H 'X-KV-Op: GET' \
  -H 'X-KV-Key: app%2Fv1%2Fusers%2Fu_1' -H 'X-KV-Op-Id: read-user'

curl -fsS -X POST "$BASE/v1/tx/client-create-1/commit" \
  -H "APIKey: $API_KEY" -H 'Content-Type: application/json' --data '{"total_ops":2}'
```

Status body (actual deadline varies):

```json
{"tx_id":"client-create-1","status":"pending","total_ops":2,"deadline":"2026-11-25T13:00:30Z","received_seq":[1],"missing_seq":[2]}
```

All fragments plus commit cause execution in seq order inside one native transaction. Internal reads see earlier writes. Final results include tx_id, status, and results[]; each item has seq/op/status/key plus applicable version/content_type/value_base64/error. Decode GET value_base64. HEAD has no value_base64. Missing GET/HEAD/DELETE abort and roll back; HTTP 409 then contains an aborted TxResult, not the normal error envelope. Missing EXISTS is an item-level 404 and does not abort or assert absence.

Commit may precede fragments; the final fragment automatically commits and returns a committed result with HTTP 202. Inspect status. Retrying an identical fragment returns 202; a committed replay returns the original result. A changed pending fragment aborts with 409 SEQ_CONFLICT; changed committed fragments return 409 TX_ALREADY_COMMITTED. Aborted/expired fragments are rejected. Repeated commit returns the same result, without re-execution.

Commit accepts an empty body, `{}`, or a single JSON object with optional total_ops/tx_digest. Nonzero total_ops must match creation. Create/commit/create-userspace bodies have a fixed 1 MiB limit; malformed JSON, null, arrays, and trailing content return 400. Unknown JSON fields are ignored.

Compatibility digest: `sha256:<lowercase hex>` of seq-sorted UTF-8 lines `<seq>\0<op_id>\0<op_type>\0<decoded-key>\0<body_hash>\n`. Here `\0` is NUL and `\n` newline; body_hash hashes raw body bytes, or empty bytes. Content-Type is not covered. A waiting commit's non-empty digest survives empty retries; a different non-empty digest returns 400 INVALID_TX.

After a lost response, query the original result or retry that commit. A new transaction is a new business attempt; do not automatically repeat an uncertain operation under a different ID.

## Admin

Admin role is required for:

```text
GET/POST /v1/admin/userspaces
DELETE   /v1/admin/userspaces/{userspace_id}
POST     /v1/admin/userspaces/{userspace_id}/api-key
GET      /v1/admin/userspaces/{userspace_id}/keys
PUT/GET/HEAD/DELETE /v1/admin/userspaces/{userspace_id}/kv/{encoded-key}
```

Creation body: `{"userspace_id":"alice","user_id":"alice"}`; user_id defaults to userspace_id. Success is 201 with user_id/userspace_id/api_key; plaintext Key is returned once. Duplicate space → 409 USERSPACE_EXISTS. Key listing returns sorted metadata without values; it does not give ordinary users a scanning API.

Rotation preserves roles and invalidates all old APIKeys for that userspace. Deletion atomically removes its KV, credentials/JWT mappings, and transactions. Bootstrap is initial setup only and does not restore rotated/deleted credentials on restart. Changing bootstrap environment variables does not rotate a Key.

## Limits, Import, and Persistence

Defaults (confirm instance configuration):

| Configuration | Default |
|---|---|
| KVHTTP_MAX_KEY_SIZE | 4096 bytes, at most 32768 |
| KVHTTP_MAX_VALUE_SIZE | 104857600 bytes per value/PUT fragment |
| KVHTTP_MAX_IMPORT_SIZE | 1073741824 bytes for the whole import |
| KVHTTP_MAX_TX_OPS | 1000 |
| KVHTTP_MAX_TX_BODY_SIZE | 268435456 bytes across one transaction's fragment bodies |
| KVHTTP_DEFAULT_TX_TIMEOUT_MS / KVHTTP_MAX_TX_TIMEOUT_MS | 30000 / 300000 |
| KVHTTP_TX_CLEAN_INTERVAL_MS | 5000 |
| KVHTTP_TX_RESULT_RETENTION_MS | 3600000 |

All numeric server configuration limits are positive. The service rejects unknown configuration names and invalid values; `--config` uses its file only, without environment merging. Relative storage paths resolve against the process working directory.

Retention prunes fragment bodies after a terminal state, keeping operation identity/hashes and full results for idempotency. It does not delete results/transactions, cap active transaction count, or guarantee file compaction. Expired retention starts at deadline; legacy records without finished_at use deadline.

Export: GET `/v1/export`, binary attachment `kv-export.bin`, magic KVHTTP01. Import: POST `/v1/import`, Content-Type application/octet-stream, body from export. X-KV-Import-Mode is replace (default), merge-overwrite, or merge-skip. Success is 200 with imported/skipped/replaced counts. Replace clears KV in the credential's own userspace only.

Checksums, record keys, Content-Type, value types, duplicate keys, sizes, and JSON are validated before import. Import is atomic, assigns new versions/updated_at, preserves nonzero created_at, and recalculates checksum/type. It does not restore original version counters. Requests exceeding the whole import limit return 413; oversized records inside a valid-size body return 400, invalid JSON returns 422. Import/export buffer the complete dataset; export has no total-size cap and can exceed the configured import cap.

Current durable file: `<KVHTTP_STORAGE_PATH>/httpkvdb.db`, an exclusively locked bbolt database with logically isolated KV/auth/transaction buckets. Legacy httpkvdb.json migrates once; old JSON and userspaces/ mirrors remain but stop updating. Full backup requires stopping the service and copying the database. KV export excludes auth and transactions and is not a full backup. Back up before destructive import/migration, not only afterward.

## Errors

Normal envelope: `{"error":"CODE","message":"...","request_id":"..."}`. Exception: failed transaction operations return HTTP 409 and an aborted TxResult. HEAD has no body. Proxies/network failures may have other shapes.

- 400: BAD_REQUEST, INVALID_KEY, INVALID_USERSPACE, INVALID_TX.
- 401: UNAUTHORIZED; 403: FORBIDDEN (role, userspace, or ownership).
- 404: KEY_NOT_FOUND, TX_NOT_FOUND, USERSPACE_NOT_FOUND, NOT_FOUND (route/method combination).
- 405: METHOD_NOT_ALLOWED for unsupported KV methods.
- 409: USERSPACE_EXISTS, SEQ_CONFLICT, TX_ALREADY_COMMITTED, TX_ABORTED, or an aborted TxResult.
- 410: TX_EXPIRED; 413: VALUE_TOO_LARGE (request or transaction aggregate limit).
- 422: INVALID_JSON; 500: STORAGE_ERROR; 503: STORAGE_ERROR on readiness failure.
