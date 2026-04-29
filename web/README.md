# httpkvdb Web Console

React + Vite frontend for `httpkvdb`.

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

## Develop

```bash
npm install
npm run dev
```

## Build

```bash
npm run build
```

The production bundle is written to `web/dist`.
