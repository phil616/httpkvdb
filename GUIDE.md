# HTTPKVDB Agent Integration Guide

This guide describes the implemented client API. The authoritative behavior specification is [docs/SPEC.md](docs/SPEC.md); deployment and backup instructions are in [docs/OPERATIONS.md](docs/OPERATIONS.md).

## Connection and Identity

Obtain the service origin (`BASE_URL`), an assigned `USERSPACE_ID`, and an APIKey or JWT from the operator. An application name such as `webapp` is a useful userspace convention, not a server requirement. Always use the userspace actually assigned to the credential.

Each authenticated Principal maps to one userspace. Multiple credentials can share that userspace. Ordinary clients cannot select a different userspace by changing a URL. Admin endpoints explicitly allow an `admin` Principal to manage other userspaces.

Send exactly one non-empty credential source:

```http
APIKey: <api_key>
X-API-Key: <api_key>
Authorization: ApiKey <api_key>
Authorization: Bearer <jwt>
```

These are alternatives. Multiple sources or repeated authentication headers return `401 UNAUTHORIZED`. Authorization schemes are case-insensitive. Do not put credentials in query strings or log credentials, JWTs, Authorization headers, or stored values.

JWT verification supports HS256 with the configured secret. `sub` and an unexpired `exp` are required; `nbf` is checked when supplied. `iss` and `aud` are checked when their expected values are configured. The server has no login or JWT issuance API and does not support JWKS. A JWT subject must already have a server-side mapping; creating a userspace through HTTP generates an APIKey only.

Optional `X-Request-Id` is returned in the response headers and error envelope. Public endpoints are `GET /healthz`, `GET /readyz`, and `GET /metrics`; HEAD is also supported. `/readyz` checks database/schema/bucket accessibility, not disk free space, durable write success, or cleaner health. The backend does not serve the Web console bundle.

## Key Encoding and Value Types

Use either ordinary KV route:

```text
/v1/kv/{encoded-key}
/api/v1/{userspace_id}/{encoded-key}
```

The first uses the authenticated userspace. The second also checks its userspace segment against the Principal and returns `403 FORBIDDEN` on mismatch. Userspace IDs match `[A-Za-z0-9][A-Za-z0-9._-]{0,127}`. Names such as `kv`, `tx`, and `admin` are valid userspaces.

Keys are non-empty UTF-8 strings, limited to 4096 bytes by default (configurable up to 32768). Percent-encode the entire logical key exactly once:

| Logical key | Encoded key |
|---|---|
| `agents/session/42` | `agents%2Fsession%2F42` |
| `a%2Fb` | `a%252Fb` |
| `100%` | `100%25` |
| `a b` | `a%20b` |
| `a+b` | `a%2Bb` |

The server decodes once and does not normalize dot segments or repeated slashes in keys. Use path encoding, not form encoding: `+` means plus, not space. Clients/proxies must preserve the encoded path. Browsers can normalize `.` and `..` even when percent-encoded; use transaction operations with `X-KV-Key` for those keys. Keys have no filesystem or directory semantics.

`X-KV-Key` in transaction fragments uses the same encoding rule: encode the complete logical key once. A percent-encoded field inside an application key is part of the logical key and its `%` must be encoded for transport too.

| Content-Type | Stored value_type |
|---|---|
| `text/plain` (parameters allowed) | string |
| `application/json` (parameters allowed) | json, validated |
| `application/octet-stream` or omitted | binary |
| Other types | binary; original Content-Type preserved |

Content-Type is limited to 65535 bytes and cannot contain CR, LF, or NUL. Ordinary PUT accepts an empty value; transaction PUT fragments require a non-empty body. Invalid JSON values return `422 INVALID_JSON`.

## Ordinary CRUD

| Method | Successful response |
|---|---|
| PUT | 200, empty body, `X-KV-Version` |
| GET | 200, raw value and metadata headers |
| HEAD | 200, metadata headers without a body |
| DELETE | 204, empty body |

GET/HEAD metadata: stored `Content-Type`, `X-KV-Version`, `X-KV-Size` in bytes, and `X-KV-Checksum` as `sha256:<hex>`. Missing GET/HEAD/DELETE keys return `404 KEY_NOT_FOUND`; there is no idempotent-delete switch. KV methods outside this set return `405 METHOD_NOT_ALLOWED`.

CRUD, imports, exports, and transaction state changes use the same global serializable lock. Reads/exports copy a consistent snapshot under that lock, then send it after releasing the lock. Multiple separate HTTP requests do not constitute one transaction.

```sh
BASE_URL="https://kvdb-api.example.com"
USERSPACE_ID="webapp"
# API_KEY must already be set privately; never enable shell tracing here.
ENCODED_KEY="$(python3 -c 'import urllib.parse; print(urllib.parse.quote("agent/smoke", safe=""))')"

curl -fsS -X PUT "$BASE_URL/api/v1/$USERSPACE_ID/$ENCODED_KEY" \
  -H "APIKey: $API_KEY" -H 'Content-Type: application/json' \
  --data '{"ok":true}'

# This prints the value; capture it privately when values are sensitive.
curl -fsS "$BASE_URL/api/v1/$USERSPACE_ID/$ENCODED_KEY" -H "APIKey: $API_KEY"
```

## Multi-Request Transactions

All transactions are bound to the creating `user_id` and `userspace_id`. The operations are fixed in advance; there is no conditional write, compare-and-set, increment, or branching on read results.

| Request | Behavior |
|---|---|
| `POST /v1/tx` | Create with `total_ops`, optional `tx_id` and `timeout_ms`; 201 even on an identical creation retry |
| `POST /v1/tx/{tx_id}/ops/{seq}` | Record fragment; 202, including when the final fragment auto-commits |
| `POST /v1/tx/{tx_id}/commit` | 200 with result when complete; 202 while missing fragments |
| `GET /v1/tx/{tx_id}/result` | 200 with status or final result, including expired/aborted status |
| `POST /v1/tx/{tx_id}/abort` | 200 with aborted status; committed cannot be aborted |

`tx_id` must match `[A-Za-z0-9_.:-]{1,128}` when supplied. Encode it as a URL segment. `timeout_ms=0` or omitted uses the default 30000 ms; negative values are invalid and the default maximum is 300000 ms. `total_ops` is positive and at most 1000 by default.

Create:

```http
POST /v1/tx
APIKey: <api_key>
Content-Type: application/json

{"tx_id":"client-operation-42","total_ops":2,"timeout_ms":30000}
```

Fragment headers:

```http
POST /v1/tx/client-operation-42/ops/1
APIKey: <api_key>
X-KV-Op: PUT
X-KV-Key: agents%2Fsession%2F42
X-KV-Op-Id: stable-fragment-id
Content-Type: application/json

{"state":"ready"}
```

`seq` ranges from 1 through `total_ops`. `X-KV-Op` accepts PUT, GET, HEAD, DELETE, or EXISTS. Every fragment needs a stable non-empty `X-KV-Op-Id`. Non-PUT fragments must have no body, including chunked bodies. No fragment reads or writes KV before execution at commit.

The fragment above can return this status (with the actual deadline):

```json
{
  "tx_id":"client-operation-42",
  "status":"pending",
  "total_ops":2,
  "deadline":"2026-11-25T13:00:30Z",
  "received_seq":[1],
  "missing_seq":[2]
}
```

Commit can arrive before the fragments. Missing fragments remain pending until the deadline; the final fragment then executes the transaction automatically. Always inspect the response `status`; a final fragment's 202 can contain a committed result.

Commit body is `{ "total_ops": 2 }`, `{}`, or empty. A supplied nonzero total must match creation. JSON control bodies (create, commit, create-userspace) have a fixed 1 MiB limit and must contain one JSON object. Null, arrays, trailing content, or malformed JSON return 400; oversize bodies return 413. Unknown JSON fields are ignored.

Operations run in seq order in one native storage transaction; internal reads see earlier writes. GET values in results use `value_base64`. A successful result has this shape:

```json
{"tx_id":"client-operation-42","status":"committed","results":[
  {"seq":1,"op":"PUT","status":200,"key":"agents/session/42","version":1},
  {"seq":2,"op":"GET","status":200,"key":"agents/session/42","content_type":"application/json","value_base64":"eyJzdGF0ZSI6InJlYWR5In0=","version":1}
]}
```

Missing GET/HEAD/DELETE keys abort and roll back the entire transaction. The HTTP response is 409 with an aborted `TxResult`, rather than the ordinary error envelope. EXISTS instead returns a per-operation 404 for an absent key and allows the transaction to continue. EXISTS is not an absence assertion and cannot enforce uniqueness.

Serializable execution does not protect an application read/compute/write sequence across separate requests. Unique indexes, increments, and list read-modify-write operations require an application coordinator covering all writers, or a separately designed conditional-write extension. Atomic writes of base and index records alone do not enforce uniqueness.

Identical fragment retries compare op ID, op type, decoded key, Content-Type, and body hash. They return 202, including original committed results. Conflicting pending fragments abort with `409 SEQ_CONFLICT`; committed transactions keep their result and return `409 TX_ALREADY_COMMITTED` for changed fragments. Aborted/expired transactions reject fragments. Repeated commit returns the identical committed result; repeated abort of an aborted transaction returns 200, while aborting an expired transaction returns 410.

An optional `tx_digest` is `sha256:<lowercase hex>` over seq-sorted UTF-8 lines:

```text
<seq>\0<op_id>\0<op_type>\0<decoded-key>\0<body_hash>\n
```

Here `\0` is a NUL byte, `\n` a newline, and body_hash is `sha256:<lowercase hex>` of raw body bytes (empty bytes for no-body operations). This compatibility digest does not include Content-Type and is not a signature of all metadata. A waiting commit's non-empty digest cannot be cleared by an empty retry or replaced with a different non-empty digest.

Each value defaults to a 100 MiB cap. One transaction's accumulated fragment bodies default to a 256 MiB cap; rejected fragments do not persist and identical retries do not consume additional capacity. After a terminal state, fragment bodies are pruned after the configured retention period (default one hour), but identities, hashes, and full results remain for retries. Pruning does not guarantee that the database file shrinks. There is no global active-transaction-count limit.

## Admin Userspaces

All these endpoints require the `admin` role:

```text
POST   /v1/admin/userspaces
GET    /v1/admin/userspaces
DELETE /v1/admin/userspaces/{userspace_id}
POST   /v1/admin/userspaces/{userspace_id}/api-key
GET    /v1/admin/userspaces/{userspace_id}/keys
PUT    /v1/admin/userspaces/{userspace_id}/kv/{encoded-key}
GET    /v1/admin/userspaces/{userspace_id}/kv/{encoded-key}
HEAD   /v1/admin/userspaces/{userspace_id}/kv/{encoded-key}
DELETE /v1/admin/userspaces/{userspace_id}/kv/{encoded-key}
```

Creation accepts `{"userspace_id":"webapp","user_id":"webapp"}`; user_id defaults to userspace_id. It returns 201 with `user_id`, `userspace_id`, and the new plaintext `api_key` once. Duplicate userspaces return 409. Listing keys returns sorted metadata without values, not a normal-user scan API.

Rotation returns the new plaintext APIKey once, invalidates all old APIKeys for that userspace, and preserves roles. Deletion atomically removes that userspace's KV, credentials, JWT mappings, and transactions. The bootstrap credential is created only during initial initialization; restart does not restore revoked credentials or deleted userspaces. Changing the configured bootstrap key is not a rotation mechanism.

## Import, Export, and Backup

`GET /v1/export` returns `application/octet-stream` with attachment filename `kv-export.bin`. The format starts with `KVHTTP01` and includes checksums. It contains only the current credential's KV records, with no credentials or transaction state.

`POST /v1/import` takes that binary body with `Content-Type: application/octet-stream` and an optional `X-KV-Import-Mode`:

| Mode | Behavior |
|---|---|
| `replace` (default) | Clear current userspace's KV, then import |
| `merge-overwrite` | Replace conflicting values |
| `merge-skip` | Preserve conflicting existing values |

Success returns 200 and `{"imported":10,"skipped":0,"replaced":0}`. Actual writes receive new global versions and updated_at timestamps; created_at is preserved when nonzero. Import does not restore the original version counter. Invalid format/checksums/duplicate keys return 400, and invalid JSON values return 422 before any KV changes.

`KVHTTP_MAX_IMPORT_SIZE` limits the whole import body (default 1 GiB, including format overhead); `KVHTTP_MAX_VALUE_SIZE` still limits every value. Import/export buffer the complete dataset in memory. Export has no total-size limit, so a large export may require a higher import limit to restore.

For a full backup, stop the service and copy `httpkvdb.db`. Before upgrading a legacy database, back up the entire stopped data directory. The legacy JSON is the migration input; old value mirrors are never used for recovery. Both become stale after migration; do not use them to undo later writes. Back up before a destructive import; an export taken after migration only archives the migrated state.

## Error Handling and Retries

Ordinary application errors use `{"error":"CODE","message":"...","request_id":"..."}`. Transaction operation failures use the aborted result described above. HEAD never includes a body. Network/proxy failures can have other shapes, so clients should not assume every response is JSON.

| HTTP | Codes / meaning |
|---|---|
| 400 | BAD_REQUEST, INVALID_KEY, INVALID_USERSPACE, INVALID_TX |
| 401 | UNAUTHORIZED |
| 403 | FORBIDDEN: userspace, role, or transaction ownership mismatch |
| 404 | KEY_NOT_FOUND, TX_NOT_FOUND, USERSPACE_NOT_FOUND, NOT_FOUND (unknown route/method combination) |
| 405 | METHOD_NOT_ALLOWED (ordinary/admin KV) |
| 409 | USERSPACE_EXISTS, SEQ_CONFLICT, TX_ALREADY_COMMITTED, TX_ABORTED; or an aborted TxResult |
| 410 | TX_EXPIRED |
| 413 | VALUE_TOO_LARGE: request or accumulated transaction body exceeds its limit |
| 422 | INVALID_JSON |
| 500 / 503 | STORAGE_ERROR (503 for failed readiness) |

When a commit response is lost, query the original tx_id or repeat the same commit; do not immediately create a new transaction and repeat a business action. Only retry a confirmed aborted/expired operation in a new transaction after revalidating application state and business idempotency. On 401 obtain valid credentials; on 403 verify the assigned userspace, role, and ownership rather than changing paths to bypass checks.
