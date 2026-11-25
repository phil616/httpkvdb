# httpkvdb Web Console

React + Vite frontend for `httpkvdb`, built with Ant Design and a feature-oriented module layout.

The console supports:

- service health and metrics
- admin userspace creation with generated APIKey display
- admin userspace listing, deletion, APIKey rotation, and granular KV management
- KV CRUD through `/v1/kv/{key}` or `/api/v1/{userspace}/{key}`
- transaction control
- binary import/export

## Structure

```text
src/app/                 application shell, responsive layout, view state
src/components/          shared UI helpers
src/features/admin/      admin userspace and metrics panel
src/features/kv/         KV CRUD panel
src/features/tx/         transaction panel
src/features/import-export/
src/lib/                 API client, persistence, shared types
```

Feature panels are lazy-loaded and Vite splits React, Ant Design, and application chunks during production builds.

For source development/builds, use Node.js 24.x and npm 11.x as declared in package.json (packageManager pins the preferred npm version). Release archives contain the built web/dist assets, not the source or npm project; deploy those assets directly.

## Configure

```bash
cp .env.example .env.local
```

Set the backend URL explicitly:

```bash
VITE_API_BASE_URL=http://127.0.0.1:8080
```

The backend must allow this frontend origin with CORS. For the default Vite dev server:

```bash
KVHTTP_CORS_ALLOWED_ORIGINS=http://127.0.0.1:5173,http://localhost:5173
```

APIKey sessions send the `APIKey` HTTP header. JWT sessions send `Authorization: Bearer <jwt>`.

The backend does not serve the production bundle; host web/dist separately with
a static server and configure VITE_API_BASE_URL at build time. Credentials remain
in memory by default; selecting the remember option stores them in browser
localStorage until cleared. Avoid that option on shared browsers.

Transaction X-KV-Key headers and ordinary URL keys use one percent-encoding pass.
The backend has no conditional write API: EXISTS does not enforce uniqueness.
See [GUIDE.md](../GUIDE.md) for transaction errors, retries, and import limits.

## Develop

```bash
npm ci
npm run dev
```

## Test

```bash
npm test
npm run lint
```

## Build

```bash
npm run build
```

The production bundle is written to `web/dist`.
