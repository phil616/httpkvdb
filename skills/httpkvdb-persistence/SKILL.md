---
name: httpkvdb-persistence
description: Design, integrate, test, or migrate application persistence using HTTPKVDB's authenticated HTTP KV API. Use for key modeling, atomic multi-key writes, userspace setup, and relational-to-KV migration; not for SQL queries or direct edits to database files.
---

# HTTPKVDB Persistence

Treat HTTPKVDB as a single-node, globally serializable HTTP KV store. Ordinary CRUD is a single-operation transaction. Multi-request transaction fragments are persisted but do not execute until commit. The service has no conditional writes, compare-and-set, increments, joins, normal-user scans, or automatic indexes.

When modifying this repository, follow `docs/SPEC.md` and inspect the relevant handlers, storage, and tests. When integrating a different application, use the references below; do not assume that application contains HTTPKVDB's source paths. Confirm the deployed service's version/configuration when behavior or limits matter.

Never log raw values, APIKeys, JWTs, or Authorization headers. Do not edit `httpkvdb.db`, the legacy JSON snapshot, or old mirrors to change application data. Use the authenticated API.

## Choose the Relevant Reference

- [API reference](references/api-reference.md): routes, encoding, statuses, limits, authentication, and persistence/backup boundaries.
- [Modeling and migration](references/modeling-and-migration.md): aggregate boundaries, explicit indexes, concurrency constraints, source-to-target mapping, and restore planning.
- [Testing and validation](references/testing-and-validation.md): behavior tests, integration checks, and migration validation.

## Inputs and Workflow

Use supplied information first. Ask for missing details only when they affect the task: the service origin and assigned userspace, credential method, source schema/entities, access patterns, expected sizes/counts, atomicity boundaries, or migration downtime/rollback requirements. Application name is a naming convention, not the authorization source. Do not infer permission to create userspaces, rotate credentials, delete data, or import in replace mode merely from possession of a credential.

Design stable, versioned logical keys for exact lookups. Use JSON for structured aggregates, text/plain for strings, and application/octet-stream for bytes. Percent-encode the complete logical key once in both HTTP paths and `X-KV-Key`; literal percent signs inside modeled keys must also be encoded for transport.

Keep base records and their application-managed index/reference keys in one transaction when they must change atomically. Before claiming uniqueness or safe read-modify-write, account for the interval between reading and constructing the transaction: all participating writers need an application coordinator. EXISTS only reports presence; it does not assert a condition or cancel later PUTs. Do not promise that atomic multi-key writes alone enforce uniqueness, foreign keys, increments, or list updates.

Submit stable seq/op IDs, then commit. A final fragment can auto-commit when commit arrived early; inspect its status even on HTTP 202. If a response is lost, query/retry the original transaction ID before considering a new business transaction. Do not turn uncertain outcomes into duplicate business actions.

Back up the target before destructive migration. A userspace KV export excludes auth and transaction state; a full database backup requires a stopped service. Describe key templates, aggregate/index examples, concurrency protocol, write/delete boundaries, size limits, validation, and rollback only to the extent needed for the user's task.

For repository changes, add/update behavior tests and run `go test ./...`. Keep the API examples, configuration template, and affected skill references synchronized with implemented behavior. Preserve the single global serializable lock and native atomic commit of KV plus transaction result.
