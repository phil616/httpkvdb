# Project Instructions for Codex

This repository implements a single-node strongly consistent KV-over-HTTP database in Go.

The authoritative specification is docs/SPEC.md.

When modifying or generating code:
- Follow docs/SPEC.md strictly.
- Prefer correctness and strong consistency over performance.
- Do not implement distributed behavior.
- Do not execute transaction fragments before commit.
- All ordinary CRUD operations are single-operation serializable transactions.
- All reads, writes, imports, exports, and transaction commits must pass through the global serializable lock.
- Never log raw values, API keys, JWTs, or Authorization headers.
- Add or update tests for every behavior change.
- Run `go test ./...` before finishing.