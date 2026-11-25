# httpkvdb

`httpkvdb` 是一个通过 HTTP 暴露的单机强一致 KV 数据库。它支持 userspace 隔离、APIKey/JWT 认证、字符串/JSON/二进制 value、多请求事务，以及二进制导入导出。

权威行为规范见 [docs/SPEC.md](docs/SPEC.md)。AI 智能体接入说明见 [GUIDE.md](GUIDE.md)。

## 特性

- 单节点强一致，优先正确性，不做分布式扩展。
- 普通 `PUT` / `GET` / `HEAD` / `DELETE` 是单操作可串行化事务。
- 普通 CRUD、事务提交、导入、导出都会经过全局串行化锁。
- 事务片段在 commit 前只持久化，不提前执行。
- 每个认证 Principal 映射到一个 userspace，多个凭据可以共享该空间；`/api/v1/{userspace}/{key}` 会校验 URL userspace 与认证结果一致。
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
- Python 3.11+ 标准库用于生产风格测试脚本，`uv` 可选

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

认证 API 使用 `/v1/*`；用户空间 KV 路径使用 `/api/v1/{userspace}/{key}`。

认证（以下四种方式任选其一，不能同时发送多种或重复 Header）：

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

存储使用本地嵌入式事务数据库 [bbolt](https://github.com/etcd-io/bbolt/tree/v1.3.11)，数据目录由 `KVHTTP_STORAGE_PATH` 指定：

```text
<storage-path>/httpkvdb.db
```

用户 KV、APIKey 摘要、JWT subject、事务片段和结果使用独立 bucket。写入使用同步持久化的原子事务；KV 更新与事务结果一起提交。Key 只作为数据库键保存，不转换为文件名，支持默认上限 4096 字节的 Key。数据库文件有独占锁，同一目录不能同时启动第二个实例。

首次打开旧数据目录时，会将 `httpkvdb.json` 中的 KV、认证、全局版本和事务状态原子迁移到新数据库。旧 JSON 和 `userspaces/` 镜像保留但不再更新；它们不是当前备份，也不能用于回滚新版本的写入。迁移保留实际存储的 Key；客户端应移除为旧版重复解码添加的二次编码补丁。迁移失败会阻止启动，不会静默创建空库。升级前请停服并备份整个数据目录；升级后的离线备份应在停服后复制 `httpkvdb.db`，在线备份使用导出 API（仅包含当前 userspace 的 KV，不包含认证和事务状态）。

相对存储路径以进程工作目录为基准；生产部署建议使用绝对路径，如 `/var/lib/httpkvdb`。导入总大小由 `KVHTTP_MAX_IMPORT_SIZE` 独立限制（默认 1 GiB，包含二进制格式开销），每条 Value 仍受 `KVHTTP_MAX_VALUE_SIZE` 限制。导入导出目前会在内存中缓冲整个数据集，应按可用内存配置上限。

首次初始化后，bootstrap 配置不会重新添加已轮换或删除的认证信息；轮换管理员 APIKey 会保留其角色。修改 bootstrap 环境变量不是轮换 Key 的方式。

事务片段累计 body 上限由 `KVHTTP_MAX_TX_BODY_SIZE` 控制，默认 256 MiB。`KVHTTP_TX_RESULT_RETENTION_MS` 控制终态片段 body 的保留期，不删除事务记录、哈希或完整结果，也不保证数据库文件缩小。没有条件写入或 CAS；`EXISTS` 不会阻止后续 PUT，应用侧的唯一性检查或读改写需要协调所有写入者。

JWT 仅支持 HS256，要求已映射的 `sub` 和有效的 `exp`；服务没有 JWT 签发/登录接口。配置的 `iss` / `aud` 非空时才检查。API 的精确状态码、重试规则、路径编码与当前实现边界见 [GUIDE.md](GUIDE.md)；所有配置名、默认值及校验规则见 [docs/SPEC.md 第 19 节](docs/SPEC.md#19-配置项)。

## 测试

标准测试：

```bash
go test ./...
```

生产风格功能测试会启动真实服务进程，通过 HTTP 验证认证、KV CRUD、事务、导入导出、指标和重启持久化：

```bash
go build -trimpath -ldflags='-s -w' -o bin/kvhttpd ./cmd/kvhttpd
python3 scripts/production_test.py --binary ./bin/kvhttpd --port 18080
```

测试还会验证 `/api/v1/{userspace}/{key}`、`APIKey` Header、管理员创建 userspace 以及特殊字符和长 Key 的访问。测试报告不会打印 APIKey、`Authorization` Header 或原始 value。
