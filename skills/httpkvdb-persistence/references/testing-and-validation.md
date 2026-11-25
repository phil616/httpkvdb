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
- Conflicting/repeated authentication headers are rejected.
- JWTs require HS256, mapped sub, and valid exp; configured issuer/audience checks apply.
- Credential rotation/deletion cannot be undone by restart or a concurrent cache fill.

Transactions:

- Fragments are persisted but not executed before commit.
- Operations execute in `seq` order at commit.
- Commit makes all writes visible atomically.
- Failed commit does not partially write.
- Duplicate identical fragment is idempotent.
- Duplicate changed fragment returns `409 SEQ_CONFLICT` and aborts.
- Non-`PUT` fragment with body returns `400 BAD_REQUEST`.
- `PUT` fragment with invalid JSON returns `422 INVALID_JSON`; empty PUT fragments return 400, while ordinary PUT accepts empty values.
- Final fragments can auto-commit with HTTP 202; identical committed fragment/commit retries return the original result without new versions.
- Concurrent fragments are not lost; concurrent commits execute once; a queued commit rechecks the deadline.
- Aggregate transaction bodies enforce KVHTTP_MAX_TX_BODY_SIZE; pruning preserves hashes/results and committed retry behavior.
- Failed GET/HEAD/DELETE returns an aborted TxResult at HTTP 409. Missing EXISTS reports an item-level 404 without aborting.
- Transaction result GET values are base64 encoded.

Import/export:

- Export includes records for the authenticated userspace only.
- Import modes work as specified: `replace`, `merge-overwrite`, `merge-skip`.
- Bad import format returns `400 BAD_REQUEST`.
- Invalid JSON inside imported JSON records returns `422 INVALID_JSON`.
- Import/export takes its storage snapshot/commit through the global serializable lock; network transfer occurs after releasing it.
- A multi-value export larger than one value can be reimported within the separate whole-import limit.
- Bad checksums/duplicate keys/unknown value types cannot partially replace existing data.
- Content-Type lengths accepted by writes round-trip through export/import.

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

- Back up source data and the target before destructive changes. A target KV export excludes credentials/transactions; full database backup requires stopping the service.
- Confirm target userspace exists.
- Confirm key/value, whole-import, operation-count, and accumulated transaction body limits are sufficient.
- Decide whether import mode is `replace`, `merge-overwrite`, or `merge-skip`.

During migration:

- Generate records deterministically.
- Percent-encode complete logical keys once for paths and X-KV-Key; test literal percent signs, encoded slashes, Unicode, and maximum-length keys.
- Use transactions for small invariant groups; use import/export for bulk loading when the generated binary format is available.
- Capture counts and checksums without logging raw sensitive values.

After migration:

- Compare source row counts to target base-record counts.
- Verify foreign-key-like references point to existing target IDs.
- Verify unique index keys resolve to exactly one base record.
- Execute the application's real read paths against HTTPKVDB.
- Run export and store the artifact according to the deployment's retention policy.

## Common Failure Modes

Stale indexes: update base/index keys atomically; coordinate any prerequisite read/compute/write flow across all writers to prevent stale pre-reads from overwriting concurrent changes.

Lost uniqueness: the server has no conditional write API. EXISTS plus PUT does not enforce absence. Coordinate all writers from pre-read through confirmed commit; an in-process lock is insufficient if other writers bypass it.

Oversized aggregate: split large embedded arrays into child records or time/hash buckets.

Accidental userspace bypass: never trust URL userspace alone; rely on authenticated principal or admin endpoints.

Leaking secrets in tests: redact API keys, JWTs, authorization headers, and stored values from logs and assertions.

## Server and Skill Maintenance

Run `go test ./...` for repository changes; use `go test -race ./...` for shared-state changes and `go vet ./...` for Go quality checks. Build the binary before running `python3 scripts/production_test.py --binary ./bin/kvhttpd --port 18080`; Python 3.11+ standard library is sufficient and uv is optional. The script uses temporary data and exercises real restart persistence.

Storage tests must close Store before reopening the same directory: the native database holds an exclusive process lock. Verify native atomic rollback, committed KV/result persistence, one-time legacy migration, invalid-file startup failure, and independent long/encoded keys without file mirrors. Do not restore tests that assert known bugs as expected behavior.

When a server contract changes, update docs/SPEC.md, GUIDE.md, affected README/operations sections, the environment template, and this skill's relevant references. Keep recommended future work separate from implemented capabilities. Validate skill frontmatter/references and run the documented API examples against a disposable instance.
