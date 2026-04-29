# Version Management

This project keeps runtime and CI tool versions in project-owned files instead of hard-coding them in several workflow steps.

## Go

Go version declarations live in `go.mod`:

```text
go 1.22
toolchain go1.26.2
```

Meaning:

- `go 1.22` is the source compatibility target.
- `toolchain go1.26.2` is the preferred build and CI toolchain.

GitHub Actions uses:

```yaml
uses: actions/setup-go@v6
with:
  go-version-file: go.mod
```

`actions/setup-go@v6` reads the `toolchain` directive when present and falls back to the `go` directive otherwise.

## Node.js and npm

Frontend runtime declarations live in `web/package.json`:

```json
{
  "packageManager": "npm@11.9.0",
  "engines": {
    "node": "24.x",
    "npm": "11.x"
  }
}
```

GitHub Actions uses:

```yaml
uses: actions/setup-node@v6
with:
  node-version-file: web/package.json
```

This keeps local frontend development and CI on the same Node major version.

## GitHub Actions

The workflows use current major action versions:

- `actions/checkout@v6`
- `actions/setup-go@v6`
- `actions/setup-node@v6`
- `github/codeql-action/*@v4`

These action major versions run on Node.js 24 and require modern GitHub Actions runners. GitHub-hosted runners satisfy that requirement.

## Upgrade Checklist

When upgrading versions:

1. Update `toolchain` in `go.mod`.
2. Run `go test ./...`.
3. Update `web/package.json` `engines` and `packageManager` if Node/npm changes.
4. Run `cd web && npm install --package-lock-only && npm run build`.
5. Keep workflow files reading from `go.mod` and `web/package.json`; do not hard-code Go or Node versions in multiple workflow steps.

