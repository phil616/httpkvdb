---
name: httpkvdb-persistence
description: Use this skill when building, modifying, testing, or migrating applications that use the httpkvdb project as a persistent store. It covers the actual HTTPKVDB API, key/value modeling, JSON/string/binary value handling, userspace authentication, transactions, import/export, test workflows, required developer inputs, and relational-schema-to-key-value migration methodology.
---

# HTTPKVDB Persistence

## Core Rules

Treat HTTPKVDB as a single-node, strongly consistent, authenticated HTTP key-value database.

Follow the project specification in `docs/SPEC.md` whenever working inside this repository. Do not invent SQL, tables, range scans, secondary indexes, MVCC, distributed behavior, or direct access to auth storage. Ordinary CRUD operations are single-operation serializable transactions. Multi-operation work must use the transaction API and must not execute transaction fragments before commit.

Use `application/json` for structured values, `text/plain` for plain strings, and `application/octet-stream` for binary values. JSON writes must be valid JSON. Never log raw values, API keys, JWTs, or `Authorization` headers.

## First Questions For The Human

Ask for missing information only when it affects correctness. For persistence design or migration work, request:

- The entities or existing relational schema, including primary keys, unique constraints, foreign keys, nullable fields, and expected row counts.
- Required access patterns: exact-key reads, lookup-by-field, list views, parent-child reads, uniqueness checks, deletes, exports, and restores.
- Consistency boundaries: which records and indexes must change atomically.
- Userspace/auth model: bootstrap admin, target userspaces, APIKey or JWT usage, and whether admin cross-userspace access is needed.
- Value size and key size expectations.
- Migration source, acceptable downtime, validation method, and rollback/export requirements.

If the human already provided enough context, proceed with conservative assumptions and state them briefly.

## Workflow

1. Read `docs/SPEC.md` and the relevant handlers before changing project behavior.
2. Decide whether the task is direct CRUD integration, transaction design, admin/userspace setup, import/export, or relational-to-KV migration.
3. Load the relevant reference:
   - API calls and wire syntax: `references/api-reference.md`
   - Data modeling and relational migration: `references/modeling-and-migration.md`
   - Testing and delivery checklist: `references/testing-and-validation.md`
4. Design stable key names before writing code. Keys are the schema.
5. Use JSON documents for structured aggregates unless the value is naturally text or binary.
6. Add explicit index keys for every non-primary-key lookup the application needs.
7. Update base records and index keys in one HTTPKVDB transaction when they must stay consistent.
8. Add or update tests for behavior changes. Run `go test ./...` before finishing repository changes.

## Key Design Principles

Prefer key templates that are stable, URL-encodable, and versioned:

```text
app/v1/{entity}/{id}
app/v1/index/{entity}/{field}/{encoded_value}
app/v1/ref/{parent_entity}/{parent_id}/{child_entity}
```

Keep keys non-empty UTF-8 strings and within the configured key-size limit. URL-encode keys when they appear in HTTP paths. Avoid making secrets, raw user input, or large payloads part of keys.

Use one JSON value per aggregate when the application normally reads or writes the data together. Split records only when independent lifecycle, size, or access patterns require it.

## Consistency Guidance

Use ordinary `PUT`, `GET`, `HEAD`, and `DELETE` for single-key operations.

Use transactions for:

- Creating a record plus one or more index keys.
- Updating a unique field and replacing its old index key.
- Deleting a record plus all owned index keys.
- Moving a child record between parents.
- Any migration batch that must be all-or-nothing.

Transaction fragments are submitted as stored operations. They are not executed until commit. Submit all fragments with stable `seq` values and unique `X-KV-Op-Id` values, then commit.

## Human-Facing Output Expectations

When proposing a persistence design, include:

- Key templates.
- Example JSON values.
- Required index keys.
- Transaction boundaries.
- Read/write/delete flows.
- Constraints enforced by HTTPKVDB versus constraints enforced by application code.
- Tests to add.

When implementing code, keep changes scoped to the project patterns, do not bypass the global serializable lock, and do not add unsupported query semantics unless explicitly requested and covered by tests/spec changes.
