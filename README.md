# httpkvdb

`httpkvdb` 是一个通过 HTTP 暴露的单机强一致 KV 数据库。它支持 userspace 隔离、APIKey/JWT 认证、字符串/JSON/二进制 value、多请求事务，以及二进制导入导出。

权威行为规范见 [docs/SPEC.md](docs/SPEC.md)。AI 智能体接入说明见 [GUIDE.md](GUIDE.md)。

## 特性

- 单节点强一致，优先正确性，不做分布式扩展。
- 普通 `PUT` / `GET` / `HEAD` / `DELETE` 是单操作可串行化事务。
- 普通 CRUD、事务提交、导入、导出都会经过全局串行化锁。
- 事务片段在 commit 前只持久化，不提前执行。
- 每个认证身份映射到独立 userspace；`/api/v1/{userspace}/{key}` 会校验 URL userspace 与认证结果一致。
- APIKey 只保存 HMAC-SHA256 摘要，不保存明文。
- 日志不得包含 APIKey、JWT、`Authorization` Header 或原始 value。

## 默认部署：Docker Compose

仓库已提供 `Dockerfile` 和 `docker-compose.yml`。默认推荐使用 Docker Compose 部署；服务监听宿主机 `8080`，数据持久化到 Docker volume `httpkvdb_data`。

先创建本机 `.env`：

```bash
cat > .env <<'EOF'
KVHTTP_BOOTSTRAP_API_KEY=replace-with-a-long-random-secret
KVHTTP_API_KEY_PEPPER=replace-with-a-long-random-api-key-pepper
KVHTTP_JWT_SECRET=replace-with-a-long-random-jwt-secret
EOF
chmod 600 .env
```

可用下面命令生成随机密钥：

```bash
openssl rand -hex 32
```

启动：

```bash
docker compose up -d --build
```

检查：

```bash
docker compose ps
curl -i http://127.0.0.1:8080/healthz
curl -i http://127.0.0.1:8080/readyz
```

写入和读取：

```bash
curl -i \
  -X PUT 'http://127.0.0.1:8080/api/v1/admin_space/profile' \
  -H 'APIKey: replace-with-a-long-random-secret' \
  -H 'Content-Type: application/json' \
  --data '{"name":"Alice"}'

curl -i \
  'http://127.0.0.1:8080/api/v1/admin_space/profile' \
  -H 'APIKey: replace-with-a-long-random-secret'
```

创建新 userspace 需要管理员凭据，响应中的 `api_key` 只返回这一次：

```bash
curl -i \
  -X POST 'http://127.0.0.1:8080/v1/admin/userspaces' \
  -H 'APIKey: replace-with-a-long-random-secret' \
  -H 'Content-Type: application/json' \
  --data '{"userspace_id":"alice","user_id":"alice"}'
```

停止但保留数据：

```bash
docker compose down
```

删除服务和持久化数据：

```bash
docker compose down -v
```

注意：`httpkvdb` 是单节点数据库。不要把同一个持久化目录挂给多个容器实例，也不要用 `docker compose --scale` 横向扩容。

## 关键配置

Docker Compose 默认从 `.env` 读取密钥，并在容器中使用：

```text
KVHTTP_ADDR=0.0.0.0:8080
KVHTTP_STORAGE_PATH=/data
KVHTTP_CORS_ALLOWED_ORIGINS=http://127.0.0.1:5173,http://localhost:5173
KVHTTP_BOOTSTRAP_USER_ID=admin
KVHTTP_BOOTSTRAP_USERSPACE_ID=admin_space
KVHTTP_BOOTSTRAP_API_KEY=<required>
KVHTTP_API_KEY_PEPPER=<required>
KVHTTP_JWT_SECRET=<required>
```

完整配置模板见 [configs/kvhttpd.env.example](configs/kvhttpd.env.example)。

生产环境至少要做这些事：

- 替换 `KVHTTP_BOOTSTRAP_API_KEY`、`KVHTTP_API_KEY_PEPPER`、`KVHTTP_JWT_SECRET`。
- 保护 `.env`，不要提交密钥。
- 按实际前端域名设置 `KVHTTP_CORS_ALLOWED_ORIGINS`。
- 对外暴露时在前面放置 TLS 和网络访问控制。

## 本地开发

要求：

- Go 1.22+ 源码兼容；首选工具链见 `go.mod`
- Node.js/npm 用于 Web 管理端
- Python 3.11+ 和 `uv` 用于生产风格测试脚本

运行后端测试：

```bash
go test ./...
```

构建后端二进制：

```bash
mkdir -p bin
go build -trimpath -ldflags='-s -w' -o bin/kvhttpd ./cmd/kvhttpd
```

本地启动后端：

```bash
KVHTTP_ADDR=127.0.0.1:8080 \
KVHTTP_STORAGE_PATH=./data \
KVHTTP_CORS_ALLOWED_ORIGINS=http://127.0.0.1:5173,http://localhost:5173 \
KVHTTP_BOOTSTRAP_API_KEY=dev-secret-key \
KVHTTP_API_KEY_PEPPER=dev-api-key-pepper \
KVHTTP_JWT_SECRET=dev-jwt-secret \
./bin/kvhttpd
```

构建 Web 管理端：

```bash
cd web
npm ci
npm run build
```

## HTTP API 快速参考

所有认证 API 都使用 `/v1` 前缀。

认证：

```http
Authorization: ApiKey <api_key>
Authorization: Bearer <jwt>
APIKey: <api_key>
X-API-Key: <api_key>
```

KV：

```text
PUT    /v1/kv/{url-encoded-key}
GET    /v1/kv/{url-encoded-key}
HEAD   /v1/kv/{url-encoded-key}
DELETE /v1/kv/{url-encoded-key}

PUT    /api/v1/{userspace}/{url-encoded-key}
GET    /api/v1/{userspace}/{url-encoded-key}
HEAD   /api/v1/{userspace}/{url-encoded-key}
DELETE /api/v1/{userspace}/{url-encoded-key}
```

管理：

```text
POST /v1/admin/userspaces
GET    /v1/admin/userspaces
DELETE /v1/admin/userspaces/{userspace}
POST   /v1/admin/userspaces/{userspace}/api-key
GET    /v1/admin/userspaces/{userspace}/keys
PUT    /v1/admin/userspaces/{userspace}/kv/{key}
GET    /v1/admin/userspaces/{userspace}/kv/{key}
HEAD   /v1/admin/userspaces/{userspace}/kv/{key}
DELETE /v1/admin/userspaces/{userspace}/kv/{key}
```

事务：

```text
POST /v1/tx
POST /v1/tx/{tx_id}/ops/{seq}
POST /v1/tx/{tx_id}/commit
GET  /v1/tx/{tx_id}/result
POST /v1/tx/{tx_id}/abort
```

导入导出：

```text
GET  /v1/export
POST /v1/import
```

健康检查和指标：

```text
GET /healthz
GET /readyz
GET /metrics
```

## 存储

当前存储后端会在 `KVHTTP_STORAGE_PATH` 下写入一个 JSON 快照文件，并同步生成按 userspace 分组的 KV 文件镜像：

```text
<storage-path>/httpkvdb.json
<storage-path>/userspaces/{userspace}/{key}.txt
<storage-path>/userspaces/{userspace}/{key}.json
<storage-path>/userspaces/{userspace}/{key}.bin
<storage-path>/userspaces/{userspace}/{key}
```

文件后缀由 `Content-Type` 决定：`text/plain` 为 `.txt`，`application/json` 为 `.json`，`application/octet-stream` 为 `.bin`，其他类型无后缀。复杂 key 会被安全编码后落盘，不会直接作为路径使用。写入通过临时文件和原子 rename 持久化。生产部署时应使用可靠的本地持久化存储。

## 测试

标准测试：

```bash
go test ./...
```

生产风格功能测试会启动真实服务进程，通过 HTTP 验证认证、KV CRUD、事务、导入导出、指标和重启持久化：

```bash
go build -trimpath -ldflags='-s -w' -o bin/kvhttpd ./cmd/kvhttpd
uv run python scripts/production_test.py --binary ./bin/kvhttpd --port 18080
```

测试还会验证 `/api/v1/{userspace}/{key}`、`APIKey` Header、管理员创建 userspace 以及 userspace 文件镜像。测试报告不会打印 APIKey、`Authorization` Header 或原始 value。
