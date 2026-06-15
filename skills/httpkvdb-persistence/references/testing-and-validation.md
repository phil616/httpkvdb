# Testing And Validation

Use this reference when adding code, tests, migration tooling, or examples that depend on HTTPKVDB persistence.

## Required Repository Validation

For code changes in this repository, run:

```bash
go test ./...
```

Add or update tests for every behavior change. Prefer focused tests near the changed package:

```text
internal/httpapi/httpapi_test.go
internal/tx/coordinator_test.go
internal/storage/*_test.go
internal/importexport/importexport_test.go
internal/auth/auth_test.go
```

## API Behavior To Test

CRUD:

- `PUT` returns `200` and `X-KV-Version`.
- `GET` returns original value, `Content-Type`, `X-KV-Version`, `X-KV-Size`, and `X-KV-Checksum`.
- `HEAD` returns metadata without body.
- `DELETE` returns `204`.
- Missing key returns `404 KEY_NOT_FOUND`.
- Invalid JSON with `Content-Type: application/json` returns `422 INVALID_JSON`.
- Oversized values return `413 VALUE_TOO_LARGE`.

Userspace/auth:

- `/api/v1/{userspace}/{key}` rejects mismatched authenticated userspace with `403 FORBIDDEN`.
- `/v1/kv/{key}` uses authenticated userspace only.
- Admin endpoints require `admin` role.
- API keys and authorization headers are not logged.

Transactions:

- Fragments are persisted but not executed before commit.
- Operations execute in `seq` order at commit.
- Commit makes all writes visible atomically.
- Failed commit does not partially write.
- Duplicate identical fragment is idempotent.
- Duplicate changed fragment returns `409 SEQ_CONFLICT` and aborts.
- Non-`PUT` fragment with body returns `400 BAD_REQUEST`.
- `PUT` fragment with invalid JSON returns `422 INVALID_JSON`.
- Transaction result GET values are base64 encoded.

Import/export:

- Export includes records for the authenticated userspace only.
- Import modes work as specified: `replace`, `merge-overwrite`, `merge-skip`.
- Bad import format returns `400 BAD_REQUEST`.
- Invalid JSON inside imported JSON records returns `422 INVALID_JSON`.
- Import/export goes through the global serializable lock.

## Integration Test Shape

Start a test server or local binary with a temporary storage directory. Use generated test API keys and never production secrets.

Typical flow:

```text
1. Start HTTPKVDB with temporary storage.
2. Confirm /healthz and /readyz.
3. Write a JSON record.
4. Read it back and compare bytes.
5. Create a transaction that writes base + index keys.
6. Confirm keys are invisible before commit and visible after commit.
7. Export, import into a fresh userspace, and verify records.
```

## Persistence Design Test Checklist

For a new application data model, test:

- Every declared key template can be generated deterministically.
- Every JSON document validates and includes stable IDs.
- Every read path can be satisfied with exact-key GET operations.
- Every required index is written, updated, and deleted with the base record.
- Deletes leave no stale index keys.
- Unique indexes reject duplicates according to the chosen application protocol.
- Migration can be rerun safely or has a documented cleanup/rollback step.

## Migration Validation

Before migration:

- Export or snapshot source data.
- Confirm target userspace exists.
- Confirm max key and value sizes are sufficient.
- Decide whether import mode is `replace`, `merge-overwrite`, or `merge-skip`.

During migration:

- Generate records deterministically.
- URL-encode keys for HTTP paths.
- Use transactions for small invariant groups; use import/export for bulk loading when the generated binary format is available.
- Capture counts and checksums without logging raw sensitive values.

After migration:

- Compare source row counts to target base-record counts.
- Verify foreign-key-like references point to existing target IDs.
- Verify unique index keys resolve to exactly one base record.
- Execute the application's real read paths against HTTPKVDB.
- Run export and store the artifact according to the deployment's retention policy.

## Common Failure Modes

Stale indexes: fix by updating base and index keys in the same transaction.

Lost uniqueness: current HTTPKVDB has no conditional write API; use an external application lock or add a tested compare-and-set/conditional-put feature before relying on race-free uniqueness.

Oversized aggregate: split large embedded arrays into child records or time/hash buckets.

Accidental userspace bypass: never trust URL userspace alone; rely on authenticated principal or admin endpoints.

Leaking secrets in tests: redact API keys, JWTs, authorization headers, and stored values from logs and assertions.
