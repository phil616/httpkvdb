# GitHub CI and Release

This repository includes GitHub Actions workflows for CodeQL and tag-based releases.

Version synchronization rules are documented in [VERSIONS.md](VERSIONS.md).

## Workflows

- `.github/workflows/codeql.yml`
  - Runs CodeQL for Go and TypeScript.
  - Uses Go version data from `go.mod`.
  - Uses Node.js version data from `web/package.json`.

- `.github/workflows/release.yml`
  - Runs on tags matching `v*`.
  - Runs Go tests and frontend build.
  - Builds release archives for Linux, macOS, and Windows.
  - Publishes `.tar.gz` archives and `.sha256` files to GitHub Releases.

## First Push

```bash
git lfs install
git add .
git status
git commit -m "Initial public release"
git remote add origin git@github.com:<owner>/<repo>.git
git push -u origin main
```

Confirm local artifacts and secrets are not staged:

- `bin/`
- `data/`
- `tmp/`
- `web/node_modules/`
- `web/dist/`
- `.env.local`
- local database snapshots
- logs

## Release

```bash
git tag v0.1.0
git push origin v0.1.0
```

