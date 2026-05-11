# httpkvdb

`httpkvdb` 是一个通过 HTTP 暴露的单机强一致 KV 数据库。它支持按用户隔离的 userspace、APIKey 和 JWT 认证、字符串 / JSON / 二进制三类 value、多 HTTP 请求聚合事务，以及二进制导入导出。

权威行为规范见 [docs/SPEC.md](docs/SPEC.md)。

GitHub CI/发布配置见 [docs/GITHUB.md](docs/GITHUB.md)，版本同步规则见 [docs/VERSIONS.md](docs/VERSIONS.md)。

## 当前实现状态

本项目优先保证正确性和一致性，不追求极限吞吐量：

- 普通 `PUT` / `GET` / `HEAD` / `DELETE` 请求会被视为单操作 Serializable 事务。
- 普通 CRUD、事务 commit、导入、导出都会经过同一把全局串行化锁。
- 事务片段提交后只会持久化，不会提前执行。
- 事务 commit 后按客户端声明的 `seq` 顺序，在一次原子存储更新中执行。
- `/api/v1/{userspace}/{key}` 会校验 URL userspace 与认证结果一致，不能用来越权指定 userspace。
- APIKey 只保存 HMAC-SHA256 摘要，不保存明文。
- 日志不得包含 APIKey、`Authorization` Header 或原始 value。

## 环境要求

- Go 1.22+ 源码兼容；首选构建工具链由 `go.mod` 指定为 Go 1.26.2
- Python 3.11+，用于运行生产风格功能测试脚本
- `uv`，用于本地运行 Python 测试脚本

## 构建

先运行 Go 测试：

```bash
go test ./...
```

构建服务端二进制：

```bash
mkdir -p bin
go build -trimpath -ldflags='-s -w' -o bin/kvhttpd ./cmd/kvhttpd
```

构建产物为：

```text
bin/kvhttpd
```

## 配置

服务支持两种配置注入方式：显式配置文件和环境变量。完整模板见 [configs/kvhttpd.env.example](configs/kvhttpd.env.example)。

本地开发可以复制一份配置：

```bash
cp configs/kvhttpd.env.example .env.local
```

编辑 `.env.local` 后，显式指定配置文件启动：

```bash
./bin/kvhttpd --config .env.local
```

当指定 `--config` 时，服务只从该文件读取配置，文件中未写的项使用内置默认值，不再从进程环境变量补齐。未指定 `--config` 时，服务回落为从环境变量读取配置。

关键配置项：

```bash
# HTTP 监听地址
KVHTTP_ADDR=0.0.0.0:8080

# 允许跨域访问的前端 Origin，多个值用英文逗号分隔
KVHTTP_CORS_ALLOWED_ORIGINS=http://127.0.0.1:5173,http://localhost:5173

# 持久化数据目录
KVHTTP_STORAGE_PATH=./data

# 限制项
KVHTTP_MAX_KEY_SIZE=4096
KVHTTP_MAX_VALUE_SIZE=104857600
KVHTTP_MAX_TX_OPS=1000

# 事务超时和清理
KVHTTP_DEFAULT_TX_TIMEOUT_MS=30000
KVHTTP_MAX_TX_TIMEOUT_MS=300000
KVHTTP_TX_CLEAN_INTERVAL_MS=5000
KVHTTP_TX_RESULT_RETENTION_MS=3600000

# 认证缓存
KVHTTP_AUTH_CACHE_TTL_MS=60000
KVHTTP_AUTH_CACHE_MAX_ENTRIES=10000

# 首次启动创建的 bootstrap 用户
KVHTTP_BOOTSTRAP_USER_ID=admin
KVHTTP_BOOTSTRAP_USERSPACE_ID=admin_space
KVHTTP_BOOTSTRAP_API_KEY=replace-with-a-long-random-local-secret
KVHTTP_API_KEY_PEPPER=replace-with-a-long-random-api-key-pepper

# JWT 校验配置，当前实现使用 HS256
KVHTTP_JWT_SECRET=replace-with-a-long-random-jwt-secret
KVHTTP_JWT_ISSUER=
KVHTTP_JWT_AUDIENCE=
```

生产环境注意事项：

- 必须替换 `KVHTTP_BOOTSTRAP_API_KEY`，默认值只适合本地开发。
- 必须替换 `KVHTTP_API_KEY_PEPPER`，APIKey 会用该服务端 secret 派生 HMAC-SHA256 摘要后存储。
- 必须替换 `KVHTTP_JWT_SECRET`，默认值只适合本地开发。
- 应把 `KVHTTP_CORS_ALLOWED_ORIGINS` 设置为允许访问后端的前端 Origin。
- `KVHTTP_STORAGE_PATH` 应指向持久化本地磁盘目录。
- 配置文件包含密钥，应设置严格权限，例如 `chmod 600`。
- 兼容 KV API `/v1/kv/{key}` 完全使用认证结果中的 userspace。
- 用户空间 KV API `/api/v1/{userspace}/{key}` 只把 URL userspace 作为路由和可读性前缀，服务端必须校验它与认证结果一致。

## 启动

本地开发启动示例：

```bash
KVHTTP_ADDR=127.0.0.1:8080 \
KVHTTP_STORAGE_PATH=./data \
KVHTTP_CORS_ALLOWED_ORIGINS=http://127.0.0.1:5173,http://localhost:5173 \
KVHTTP_BOOTSTRAP_API_KEY=dev-secret-key \
KVHTTP_API_KEY_PEPPER=dev-api-key-pepper \
./bin/kvhttpd
```

健康检查：

```bash
curl -i http://127.0.0.1:8080/healthz
curl -i http://127.0.0.1:8080/readyz
```

写入和读取示例：

```bash
curl -i \
  -X PUT 'http://127.0.0.1:8080/api/v1/admin_space/profile' \
  -H 'APIKey: dev-secret-key' \
  -H 'Content-Type: application/json' \
  --data '{"name":"Alice"}'

curl -i \
  'http://127.0.0.1:8080/api/v1/admin_space/profile' \
  -H 'APIKey: dev-secret-key'
```

## 部署

### Docker Compose 部署

本仓库提供 `Dockerfile` 和 `docker-compose.yml`。Compose 会构建镜像、监听宿主机 `8080` 端口，并把数据库文件持久化到 Docker volume `httpkvdb_data`。

先创建只保存在本机的 `.env` 文件：

```bash
cat > .env <<'EOF'
KVHTTP_BOOTSTRAP_API_KEY=replace-with-a-long-random-secret
KVHTTP_API_KEY_PEPPER=replace-with-a-long-random-api-key-pepper
KVHTTP_JWT_SECRET=replace-with-a-long-random-jwt-secret
EOF
chmod 600 .env
```

可以用下面的命令生成随机密钥值：

```bash
openssl rand -hex 32
```

启动服务：

```bash
docker compose up -d --build
```

检查服务状态：

```bash
docker compose ps
curl -i http://127.0.0.1:8080/healthz
curl -i http://127.0.0.1:8080/readyz
```

写入和读取示例：

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

管理员创建 userspace 示例，响应中的 `api_key` 只返回这一次：

```bash
curl -i \
  -X POST 'http://127.0.0.1:8080/v1/admin/userspaces' \
  -H 'APIKey: replace-with-a-long-random-secret' \
  -H 'Content-Type: application/json' \
  --data '{"userspace_id":"alice","user_id":"alice"}'
```

停止服务但保留数据：

```bash
docker compose down
```

删除服务和持久化数据：

```bash
docker compose down -v
```

注意：`httpkvdb` 是单节点数据库，生产环境不要把同一个持久化目录挂给多个容器实例，也不要用 Compose 的 `--scale` 横向扩容该服务。

### 二进制部署

1. 在目标机器或 CI 中构建：

   ```bash
   go test ./...
   go build -trimpath -ldflags='-s -w' -o bin/kvhttpd ./cmd/kvhttpd
   ```

2. 安装二进制：

   ```bash
   sudo install -m 0755 bin/kvhttpd /usr/local/bin/kvhttpd
   ```

3. 创建数据和配置目录：

   ```bash
   sudo mkdir -p /var/lib/httpkvdb /etc/httpkvdb
   sudo chmod 700 /var/lib/httpkvdb /etc/httpkvdb
   ```

4. 安装并编辑配置：

   ```bash
   sudo cp configs/kvhttpd.env.example /etc/httpkvdb/kvhttpd.env
   sudo chmod 600 /etc/httpkvdb/kvhttpd.env
   sudo editor /etc/httpkvdb/kvhttpd.env
   ```

5. 手动启动：

   ```bash
   /usr/local/bin/kvhttpd --config /etc/httpkvdb/kvhttpd.env
   ```

### systemd 示例

创建 `/etc/systemd/system/kvhttpd.service`：

```ini
[Unit]
Description=httpkvdb single-node HTTP KV database
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/kvhttpd --config /etc/httpkvdb/kvhttpd.env
Restart=on-failure
RestartSec=2s
User=kvhttpd
Group=kvhttpd
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ReadWritePaths=/var/lib/httpkvdb

[Install]
WantedBy=multi-user.target
```

创建用户并启动服务：

```bash
sudo useradd --system --home /var/lib/httpkvdb --shell /usr/sbin/nologin kvhttpd
sudo chown -R kvhttpd:kvhttpd /var/lib/httpkvdb
sudo systemctl daemon-reload
sudo systemctl enable --now kvhttpd
sudo systemctl status kvhttpd
```

## 测试

### Go 单元和集成测试

```bash
go test ./...
```

测试覆盖内容包括：

- 存储持久化
- userspace 隔离
- APIKey / JWT 身份映射
- auth cache 行为
- 事务 seq 排序
- 事务片段幂等
- seq 冲突 abort
- 事务 rollback
- 事务超时过期
- 导入导出 checksum
- HTTP API 集成路径

### 生产风格功能测试

生产风格测试会使用构建后的二进制，启动真实服务进程，通过 HTTP 调用接口，重启服务，并输出 JSON 测试报告。

先构建：

```bash
go build -trimpath -ldflags='-s -w' -o bin/kvhttpd ./cmd/kvhttpd
```

运行测试：

```bash
uv run python scripts/production_test.py --binary ./bin/kvhttpd --port 18080
```

脚本会验证：

- `/healthz` 和 `/readyz`
- 未认证请求会被拒绝
- JSON KV CRUD 和元信息响应头
- 非法 JSON 返回 `422`
- `/api/v1/{userspace}/{key}` 与 `APIKey` Header
- 管理员创建 userspace 并生成 APIKey
- userspace 文件镜像
- 二进制 value 往返
- 事务片段在 commit 前不可见
- 乱序事务片段按 `seq` 执行
- 重复 commit 返回相同 committed result
- 二进制 export/import 模式
- `/metrics`
- 已提交数据在进程重启后仍存在

报告不会打印 APIKey、`Authorization` Header 或原始 value。

如果需要保留临时数据目录用于排查：

```bash
uv run python scripts/production_test.py --binary ./bin/kvhttpd --port 18080 --keep-data
```

## HTTP API 快速参考

所有需要认证的 API 使用 `/v1` 前缀。

认证方式：

```http
Authorization: ApiKey <api_key>
Authorization: Bearer <jwt>
APIKey: <api_key>
X-API-Key: <api_key>
```

KV API：

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

管理 API：

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

事务 API：

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

可观测性：

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

文件中包含逻辑隔离的数据区：

- 用户 KV 空间
- 系统 APIKey 记录
- 系统 JWT subject 记录
- 事务状态和已提交事务结果

文件后缀由 `Content-Type` 决定：`text/plain` 为 `.txt`，`application/json` 为 `.json`，`application/octet-stream` 为 `.bin`，其他类型无后缀。复杂 key 会被安全编码后落盘，不会直接作为路径使用。写入通过临时文件和原子 rename 持久化。生产部署时应把 `KVHTTP_STORAGE_PATH` 放在可靠的本地持久化存储上。

## 安全检查清单

- 对外暴露服务前替换所有默认密钥。
- 限制配置文件和存储目录权限。
- 如果服务不只绑定 localhost，应在前面放置 TLS 和网络访问控制。
- 不要记录请求 body、APIKey、JWT 或 `Authorization` Header。
- 创建正式运营 userspace 后，应轮换或收紧 bootstrap APIKey 的使用范围。
