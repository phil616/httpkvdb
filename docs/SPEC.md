# KV over HTTP 单机强一致存储数据库技术规格说明书

版本：v0.1  
目标读者：Codex / Go 开发者 / 项目维护者  
实现语言：Go  
系统类型：单机 HTTP KV 数据库  
核心目标：通过 HTTP 操作字符串 Key 与多类型 Value，并在单机环境下提供强一致事务能力。

---

## 1. 项目目标

本项目需要实现一个通过 HTTP 访问的 KV 存储数据库。系统不追求高并发性能，也不计划做分布式扩展，首要目标是：

1. 单机强一致性。
2. 支持多用户 userspace 隔离。
3. 支持 HTTP CRUD。
4. 支持字符串、JSON、二进制三类 Value。
5. 支持跨多个 HTTP 请求聚合形成一个事务。
6. 支持 JWT 与 APIKey 两类认证。
7. 认证数据与普通 KV 数据隔离。
8. 支持二进制导入导出。
9. 提供轻量级可观测性。

本项目应被实现为一个可以独立运行的 HTTP 服务进程。

---

## 2. 非目标

以下能力不属于本阶段目标：

1. 不实现分布式一致性。
2. 不实现 Raft、Paxos、主从复制或多节点同步。
3. 不实现复杂查询语言。
4. 不实现范围扫描、二级索引或 SQL。
5. 不实现多版本并发控制 MVCC。
6. 不追求极限吞吐量。
7. 不要求事务长时间挂起。
8. 不实现严格审计日志。
9. 不允许客户端直接访问系统认证存储。

---

## 3. 一致性原则

系统必须采用强一致性模型。

本阶段使用最简单、最可验证的方案：

> 所有读写、事务提交、导入、导出都经过同一个全局串行化锁。

即：

```text
GET / PUT / DELETE / TRANSACTION COMMIT / IMPORT / EXPORT
        ↓
Global Serializable Lock
        ↓
Storage Operation
        ↓
Durable Commit
```

### 3.1 Serializable 语义

系统对所有客户端暴露 Serializable 行为：

1. 任意两个事务之间不可交错执行。
2. 普通 CRUD 请求视为单操作事务。
3. 事务内部操作按客户端声明的序号 `seq` 顺序执行。
4. 读操作也纳入事务顺序。
5. 事务提交前，事务中的中间写入对外不可见。
6. 事务提交后，所有写入一次性可见。
7. 事务失败时不得出现部分提交。

---

## 4. 数据模型

### 4.1 Key

Key 必须是字符串。

约束：

| 项目 | 规则 |
|---|---|
| 类型 | UTF-8 字符串 |
| 最大长度 | 默认 4096 字节，可配置 |
| 是否允许空字符串 | 不允许 |
| 是否允许 `/` | 允许，但 HTTP path 中需要 URL encode |
| 是否允许二进制 Key | 不允许 |

服务端内部不得直接把 key 当作文件路径使用。必须通过编码、转义、哈希或底层 KV 引擎保存。

### 4.2 Value

Value 统一以字节数组保存。

每条 KV 记录必须保存：

```text
KVRecord
- userspace_id: string
- key: string
- value: []byte
- content_type: string
- value_type: string
- version: uint64
- created_at: timestamp
- updated_at: timestamp
- checksum: string
```

### 4.3 Value 类型推断

Value 类型由 HTTP `Content-Type` 决定。

| Content-Type | value_type |
|---|---|
| `text/plain` | `string` |
| `application/json` | `json` |
| `application/octet-stream` | `binary` |
| 其他类型 | `binary`，但保留原始 Content-Type |

对于 `application/json`，服务端必须进行 JSON 格式合法性校验。校验失败返回 `422 INVALID_JSON`。

对于 `text/plain`，服务端不需要解析编码，但建议默认认为是 UTF-8 文本。

对于二进制数据，服务端不得尝试解析内容。

---

## 5. Userspace 模型

### 5.1 基本规则

每个认证用户对应一个完整独立的 userspace。

相同 key 在不同 userspace 中互不影响：

```text
userspace_a: key1 -> value_a
userspace_b: key1 -> value_b
```

普通 KV 请求不能由客户端指定 userspace。userspace 必须由认证结果解析得到。

### 5.2 Principal

认证中间件需要把 JWT 或 APIKey 解析为统一身份对象：

```text
Principal
- user_id: string
- userspace_id: string
- roles: []string
- auth_method: jwt | apikey
```

后续业务逻辑只能依赖 Principal，不直接解析 Authorization 头。

### 5.3 系统认证空间

认证数据必须与普通 KV 数据隔离。

建议逻辑上分为：

```text
System Auth Space
- users
- api_keys
- jwt_config
- revoked_tokens 或 token_version

User KV Space
- userspace_id -> key -> value
```

普通用户不能通过 `/v1/kv`、`/v1/export`、`/v1/import` 访问认证空间。

---

## 6. 存储设计

### 6.1 存储要求

存储层必须满足：

1. 单机持久化。
2. 进程重启后数据不丢失。
3. 写入成功返回前必须完成持久化确认。
4. 事务提交必须原子化。
5. 事务失败不得部分写入。
6. 认证数据与业务数据隔离。

### 6.2 推荐目录结构

```text
/data
  /system
    auth.db
  /userspaces
    kv.db
  /tx
    tx_state.db
  /exports
    tmp/
```

实现时可以使用单个本地嵌入式 KV 数据库文件，也可以拆成多个文件。无论使用哪种方式，都必须保持逻辑隔离。

推荐逻辑 bucket / namespace：

```text
system:users
system:api_keys
system:jwt
kv:{userspace_id}
tx:states
```

### 6.3 版本号

每次成功写入或删除都应递增全局版本号或 userspace 内版本号。

建议：

```text
version: uint64
```

普通 GET 返回：

```http
X-KV-Version: <version>
```

### 6.4 Checksum

每条 value 建议保存 SHA-256 checksum，用于导出校验和故障诊断。

```text
checksum = sha256(value)
```

---

## 7. HTTP API 设计

所有 API 默认路径前缀：

```text
/v1
```

所有需要身份的 API 必须通过认证中间件。

---

## 8. 普通 KV API

普通 KV API 视为单操作事务，也必须进入全局锁。

### 8.1 写入或覆盖

```http
PUT /v1/kv/{key}
Authorization: Bearer <jwt>
Content-Type: application/json

{"hello":"world"}
```

成功：

```http
HTTP/1.1 200 OK
X-KV-Version: 12
```

语义：

1. 在当前 Principal 对应 userspace 中写入 key。
2. 如果 key 已存在，则覆盖。
3. Content-Type 作为 value 元信息保存。
4. JSON 类型必须校验合法性。
5. 成功返回前必须完成持久化。

### 8.2 读取

```http
GET /v1/kv/{key}
Authorization: Bearer <jwt>
```

成功：

```http
HTTP/1.1 200 OK
Content-Type: application/json
X-KV-Version: 12
X-KV-Size: 17
X-KV-Checksum: sha256:xxxx

{"hello":"world"}
```

不存在：

```http
HTTP/1.1 404 Not Found
Content-Type: application/json

{
  "error": "KEY_NOT_FOUND",
  "message": "key not found",
  "request_id": "req_xxx"
}
```

### 8.3 删除

```http
DELETE /v1/kv/{key}
Authorization: ApiKey <api_key>
```

成功：

```http
HTTP/1.1 204 No Content
```

如果 key 不存在，建议返回 404。也可以配置为幂等删除并返回 204。本项目默认采用严格语义：不存在返回 404。

### 8.4 元信息查询

```http
HEAD /v1/kv/{key}
Authorization: Bearer <jwt>
```

成功：

```http
HTTP/1.1 200 OK
Content-Type: application/json
X-KV-Version: 12
X-KV-Size: 17
X-KV-Checksum: sha256:xxxx
```

---

## 9. 事务协议设计

HTTP 是无状态协议，不能依赖长连接表达事务。因此系统必须提供基于事务 ID 的聚合机制。

核心机制：

```text
tx_id + seq + total_ops + op_id + body_hash
```

服务端先收集事务片段，不立即执行。只有在操作到齐并收到 commit 后，才按 seq 排序并在全局锁内执行。

---

## 10. 事务对象模型

### 10.1 Transaction

```text
Transaction
- tx_id: string
- user_id: string
- userspace_id: string
- total_ops: int
- status: pending | waiting_for_ops | ready | committing | committed | aborted | expired
- created_at: timestamp
- deadline: timestamp
- commit_received: bool
- tx_digest: string optional
- ops: map[int]TxOperation
- result: TxResult optional
- abort_reason: string optional
```

### 10.2 TxOperation

```text
TxOperation
- tx_id: string
- seq: int
- op_id: string
- op_type: GET | PUT | DELETE | EXISTS | HEAD
- key: string
- content_type: string optional
- body: []byte optional
- body_hash: string
- received_at: timestamp
```

### 10.3 TxResult

```text
TxResult
- tx_id: string
- status: committed | aborted | expired
- results: []TxOperationResult
```

### 10.4 TxOperationResult

```text
TxOperationResult
- seq: int
- op: string
- status: int
- key: string
- content_type: string optional
- value_base64: string optional
- version: uint64 optional
- error: string optional
```

事务结果中的 value 使用 base64 编码，避免 JSON 响应无法承载任意二进制。

---

## 11. 事务 API

### 11.1 创建事务

```http
POST /v1/tx
Authorization: Bearer <jwt>
Content-Type: application/json

{
  "tx_id": "optional-client-generated-uuid",
  "total_ops": 3,
  "timeout_ms": 30000
}
```

成功：

```http
HTTP/1.1 201 Created
Content-Type: application/json

{
  "tx_id": "tx_abc123",
  "status": "pending",
  "total_ops": 3,
  "deadline": "2026-04-29T12:00:30Z"
}
```

规则：

1. `total_ops` 必须大于 0。
2. `total_ops` 不得超过配置上限，例如 1000。
3. `timeout_ms` 不得超过配置上限，例如 300000。
4. 如果客户端提供 `tx_id`，服务端必须校验其格式和唯一性。
5. tx_id 一旦创建，必须绑定当前 user_id 和 userspace_id。
6. 其他用户不能向该 tx_id 提交片段。

### 11.2 提交事务片段

```http
POST /v1/tx/{tx_id}/ops/{seq}
Authorization: Bearer <jwt>
X-KV-Op: PUT
X-KV-Key: profile
X-KV-Op-Id: op-uuid-1
Content-Type: application/json

{"name":"Alice"}
```

成功：

```http
HTTP/1.1 202 Accepted
Content-Type: application/json

{
  "tx_id": "tx_abc123",
  "seq": 1,
  "status": "accepted"
}
```

规则：

1. `seq` 从 1 开始。
2. `seq` 必须小于等于 `total_ops`。
3. `X-KV-Op` 必须是 `GET`、`PUT`、`DELETE`、`EXISTS`、`HEAD` 之一。
4. `X-KV-Key` 必须是合法字符串 key。
5. `X-KV-Op-Id` 用于幂等重试，不能为空。
6. PUT 必须携带 body。
7. GET / DELETE / EXISTS / HEAD 不应该携带 body。
8. JSON 类型 PUT 必须校验 JSON 合法性。
9. 片段提交后不得立即执行。
10. 片段必须持久化到事务状态存储，避免进程重启后事务状态完全丢失。

### 11.3 事务片段冲突处理

如果同一个 `tx_id + seq` 重复提交，服务端应比较：

```text
op_id
op_type
key
content_type
body_hash
```

如果完全一致，返回 202，视为幂等重传。

如果不一致，事务必须进入 aborted，返回：

```http
HTTP/1.1 409 Conflict
Content-Type: application/json

{
  "error": "SEQ_CONFLICT",
  "message": "same tx_id and seq received different operation content",
  "request_id": "req_xxx"
}
```

### 11.4 提交事务

```http
POST /v1/tx/{tx_id}/commit
Authorization: Bearer <jwt>
Content-Type: application/json

{
  "total_ops": 3,
  "tx_digest": "sha256:optional"
}
```

如果所有操作已经到齐，则立即执行并返回结果。

成功：

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "tx_id": "tx_abc123",
  "status": "committed",
  "results": [
    {
      "seq": 1,
      "op": "PUT",
      "status": 200,
      "key": "profile",
      "version": 13
    },
    {
      "seq": 2,
      "op": "GET",
      "status": 200,
      "key": "profile",
      "content_type": "application/json",
      "value_base64": "eyJuYW1lIjoiQWxpY2UifQ==",
      "version": 13
    }
  ]
}
```

如果 commit 先到，但片段未到齐：

```http
HTTP/1.1 202 Accepted
Content-Type: application/json

{
  "tx_id": "tx_abc123",
  "status": "waiting_for_ops",
  "missing_seq": [2, 3],
  "deadline": "2026-04-29T12:00:30Z"
}
```

规则：

1. commit 请求可以早于部分片段到达。
2. commit 不会立即失败，除非事务已经 aborted、expired 或身份不匹配。
3. 一旦 commit_received=true，事务等待缺失片段直到 deadline。
4. 所有片段到齐后，服务端应自动执行事务。
5. 执行结果可以通过本次 commit 响应返回，也可以之后通过 result API 查询。

### 11.5 查询事务状态或结果

```http
GET /v1/tx/{tx_id}/result
Authorization: Bearer <jwt>
```

Pending：

```json
{
  "tx_id": "tx_abc123",
  "status": "pending",
  "received_seq": [1],
  "missing_seq": [2, 3]
}
```

Committed：

```json
{
  "tx_id": "tx_abc123",
  "status": "committed",
  "results": []
}
```

Expired：

```json
{
  "tx_id": "tx_abc123",
  "status": "expired"
}
```

### 11.6 主动中止事务

```http
POST /v1/tx/{tx_id}/abort
Authorization: Bearer <jwt>
```

成功：

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "tx_id": "tx_abc123",
  "status": "aborted"
}
```

已经 committed 的事务不能 abort。

---

## 12. 事务执行语义

### 12.1 执行条件

事务只有满足以下条件才能执行：

1. status 不是 aborted。
2. status 不是 expired。
3. commit_received = true。
4. 当前时间未超过 deadline。
5. seq 1 到 total_ops 全部存在。
6. 所有片段属于同一个 user_id 和 userspace_id。
7. 没有 seq 冲突。
8. tx_digest 如果存在，必须校验通过。

### 12.2 执行流程

```text
1. 加载 Transaction
2. 校验 ready 条件
3. 按 seq 从小到大排序 ops
4. 获取 Global Serializable Lock
5. 开启底层存储事务
6. 依次执行每个 op
7. 记录每个 op 的结果
8. 如果全部成功，提交底层存储事务
9. 持久化 tx result
10. 标记 tx.status = committed
11. 释放 Global Serializable Lock
```

如果第 6 步任意操作失败：

```text
1. 回滚底层存储事务
2. 标记 tx.status = aborted
3. 保存 abort_reason
4. 返回错误结果
```

### 12.3 事务内读写规则

事务内部按 seq 顺序执行。

示例：

```text
1. PUT a = 1
2. GET a
3. PUT a = 2
4. GET a
```

结果必须是：

```text
seq=2 读到 1
seq=4 读到 2
```

事务外部不能看到 `a=1` 的中间状态，只能看到事务提交前状态或提交后最终状态。

---

## 13. 超时与清理

### 13.1 超时规则

事务创建时必须设置 deadline。

默认配置：

```text
default_tx_timeout_ms = 30000
max_tx_timeout_ms = 300000
```

超过 deadline 后：

```text
pending / waiting_for_ops -> expired
```

expired 事务不能再接收 ops 或 commit。

### 13.2 后台清理

服务端应有后台 goroutine 定期扫描事务状态。

清理逻辑：

1. 找到超过 deadline 的 pending / waiting_for_ops 事务。
2. 标记为 expired。
3. 可保留结果或错误状态一段时间供客户端查询。
4. 超过 result retention 后删除事务片段 body，释放空间。

推荐配置：

```text
tx_clean_interval_ms = 5000
tx_result_retention_ms = 3600000
```

---

## 14. 重传与 fallback

### 14.1 客户端重传

客户端可以安全重传：

1. 创建事务请求。
2. 提交事务片段请求。
3. commit 请求。
4. result 查询请求。

服务端必须保证：

1. 相同 tx_id 的创建请求幂等。
2. 相同 tx_id + seq + 相同内容的片段提交幂等。
3. 相同 tx_id 的 commit 幂等。
4. committed 事务重复 commit 应返回相同结果。

### 14.2 fallback 语义

系统不提供自动业务补偿。

事务失败后的 fallback 是：

1. 如果事务未执行：直接 expired 或 aborted，不影响 KV。
2. 如果事务执行中失败：底层事务 rollback，不影响 KV。
3. 如果服务重启：恢复 tx 状态，未 committed 的事务按规则 expired 或等待客户端查询。
4. 客户端可创建新事务重试。

---

## 15. 认证设计

### 15.1 支持方式

系统支持：

1. JWT
2. APIKey

Authorization 格式：

```http
Authorization: Bearer <jwt>
```

或：

```http
Authorization: ApiKey <api_key>
```

### 15.2 JWT 验证

JWT 验证流程：

1. 解析 Authorization Header。
2. 验证 Bearer token 格式。
3. 校验签名。
4. 校验 exp、nbf、iss、aud。
5. 提取 subject。
6. 从系统认证空间映射到 user_id 和 userspace_id。
7. 生成 Principal。

### 15.3 APIKey 验证

APIKey 验证流程：

1. 解析 Authorization Header。
2. 提取 APIKey 明文。
3. 计算 hash。
4. 查询系统认证空间中的 api_key_hash。
5. 校验 key 状态。
6. 映射到 user_id 和 userspace_id。
7. 生成 Principal。

系统不得保存 APIKey 明文。

### 15.4 Auth Cache

为了避免每个请求都读硬盘，必须实现认证缓存。

推荐缓存对象：

```text
api_key_hash -> Principal
jwt_subject -> Principal
jwt_kid -> verification_key
user_id -> userspace_id
```

缓存策略：

```text
auth_cache_ttl = 60s
auth_cache_max_entries = 10000
```

认证数据变更时应主动失效相关缓存。

如果暂时不实现认证数据管理 API，也必须在内部代码中设计 cache invalidation 接口，便于未来扩展。

---

## 16. 导入导出设计

导入导出必须使用二进制格式。

### 16.1 导出 API

```http
GET /v1/export
Authorization: Bearer <jwt>
Accept: application/octet-stream
```

成功：

```http
HTTP/1.1 200 OK
Content-Type: application/octet-stream
Content-Disposition: attachment; filename="kv-export.bin"

<binary>
```

语义：

1. 导出当前 Principal 对应 userspace 的全部 KV 数据。
2. 导出必须进入全局锁。
3. 导出过程中不得看到部分事务状态。
4. 导出结果包含 key、value、content_type、version、timestamp、checksum。

### 16.2 导入 API

```http
POST /v1/import
Authorization: Bearer <jwt>
Content-Type: application/octet-stream
X-KV-Import-Mode: replace

<binary>
```

支持模式：

| 模式 | 语义 |
|---|---|
| `replace` | 清空当前 userspace 后导入 |
| `merge-overwrite` | 合并，冲突 key 覆盖 |
| `merge-skip` | 合并，冲突 key 保留原值 |

导入规则：

1. 先完整读取并校验二进制格式。
2. 校验 magic、version、record_count、checksum。
3. 校验每条 record 的 key、content_type、value checksum。
4. 校验成功后才进入全局锁。
5. 在底层存储事务中应用导入。
6. 成功提交后返回导入统计。
7. 任意失败必须 rollback。

### 16.3 二进制格式

建议格式：

```text
Header
- magic: 8 bytes, fixed: KVHTTP01
- format_version: uint32
- created_at_unix_ms: int64
- record_count: uint64
- header_checksum: [32]byte optional

Record[]
- key_len: uint32
- key_bytes
- content_type_len: uint16
- content_type_bytes
- value_type: uint8
- value_len: uint64
- value_bytes
- version: uint64
- created_at_unix_ms: int64
- updated_at_unix_ms: int64
- value_checksum: [32]byte

Footer
- full_checksum: [32]byte
```

所有整数建议使用 Big Endian 或 Little Endian，但必须在格式说明中固定。建议使用 Big Endian。

---

## 17. 可观测性

### 17.1 日志

日志必须避免记录 value 内容。

建议记录：

```text
request_id
method
path_template
status_code
latency_ms
user_id_hash
userspace_id_hash
tx_id
op_count
value_size
error_code
```

关键事件：

```text
server_start
server_stop
auth_failed
auth_cache_miss
tx_created
tx_committed
tx_aborted
tx_expired
import_started
import_finished
export_finished
storage_error
```

### 17.2 Metrics

提供 Prometheus 风格指标接口：

```http
GET /metrics
```

建议指标：

```text
http_requests_total
http_request_duration_seconds
kv_get_total
kv_put_total
kv_delete_total
tx_created_total
tx_committed_total
tx_aborted_total
tx_expired_total
auth_cache_hit_total
auth_cache_miss_total
global_lock_wait_seconds
storage_operation_duration_seconds
import_total
export_total
```

### 17.3 Health Check

```http
GET /healthz
```

只表示进程存活。

```http
GET /readyz
```

需要检查：

1. 存储是否可访问。
2. 系统认证空间是否可访问。
3. 后台清理任务是否已启动。

---

## 18. 错误格式

所有 JSON 错误响应必须使用统一格式：

```json
{
  "error": "ERROR_CODE",
  "message": "human readable message",
  "request_id": "req_xxx"
}
```

常见错误码：

| HTTP | error | 含义 |
|---|---|---|
| 400 | `BAD_REQUEST` | 请求格式错误 |
| 400 | `INVALID_KEY` | key 不合法 |
| 400 | `INVALID_TX` | 事务参数不合法 |
| 401 | `UNAUTHORIZED` | 未认证或认证失败 |
| 403 | `FORBIDDEN` | 无权限 |
| 404 | `KEY_NOT_FOUND` | key 不存在 |
| 404 | `TX_NOT_FOUND` | 事务不存在 |
| 409 | `SEQ_CONFLICT` | 同一事务序号内容冲突 |
| 409 | `TX_ALREADY_COMMITTED` | 事务已提交 |
| 409 | `TX_ABORTED` | 事务已中止 |
| 410 | `TX_EXPIRED` | 事务已过期 |
| 413 | `VALUE_TOO_LARGE` | value 过大 |
| 422 | `INVALID_JSON` | JSON Value 不合法 |
| 500 | `STORAGE_ERROR` | 存储错误 |

---

## 19. 配置项

服务必须支持配置文件或环境变量。

建议配置：

```text
server.addr = "0.0.0.0:8080"
storage.path = "./data"
max_key_size = 4096
max_value_size = 104857600
max_tx_ops = 1000
default_tx_timeout_ms = 30000
max_tx_timeout_ms = 300000
tx_clean_interval_ms = 5000
tx_result_retention_ms = 3600000
auth_cache_ttl_ms = 60000
auth_cache_max_entries = 10000
log.level = "info"
metrics.enabled = true
```

---

## 20. 建议 Go 项目结构

```text
.
├── cmd/
│   └── kvhttpd/
│       └── main.go
├── internal/
│   ├── auth/
│   │   ├── middleware.go
│   │   ├── jwt.go
│   │   ├── apikey.go
│   │   └── cache.go
│   ├── config/
│   │   └── config.go
│   ├── httpapi/
│   │   ├── router.go
│   │   ├── kv_handlers.go
│   │   ├── tx_handlers.go
│   │   ├── import_export_handlers.go
│   │   └── errors.go
│   ├── model/
│   │   ├── kv.go
│   │   ├── tx.go
│   │   └── principal.go
│   ├── storage/
│   │   ├── storage.go
│   │   ├── kv_store.go
│   │   ├── auth_store.go
│   │   └── tx_store.go
│   ├── tx/
│   │   ├── coordinator.go
│   │   ├── executor.go
│   │   ├── cleaner.go
│   │   └── digest.go
│   ├── importexport/
│   │   ├── encode.go
│   │   ├── decode.go
│   │   └── format.go
│   ├── observe/
│   │   ├── logger.go
│   │   ├── metrics.go
│   │   └── request_id.go
│   └── lock/
│       └── serial.go
├── docs/
│   └── SPEC.md
├── AGENTS.md
├── go.mod
└── README.md
```

---

## 21. 关键接口边界

### 21.1 Storage 接口

存储层应暴露抽象接口，业务层不得直接操作具体数据库文件。

```text
Storage
- Get(userspace_id, key) -> KVRecord
- Put(userspace_id, key, value, content_type) -> version
- Delete(userspace_id, key) -> version
- Exists(userspace_id, key) -> bool
- ExportUserspace(userspace_id) -> []KVRecord
- ImportUserspace(userspace_id, records, mode) -> ImportResult
- BeginAtomic() -> AtomicTx
```

### 21.2 AuthStore 接口

```text
AuthStore
- ResolveAPIKeyHash(hash) -> Principal
- ResolveJWTSubject(subject) -> Principal
- GetJWTVerificationConfig(kid) -> KeyConfig
```

### 21.3 TxCoordinator 接口

```text
TxCoordinator
- CreateTx(principal, total_ops, timeout, optional_tx_id) -> Transaction
- AddOp(principal, tx_id, seq, op) -> TxStatus
- Commit(principal, tx_id, total_ops, optional_digest) -> TxResult or TxStatus
- Abort(principal, tx_id) -> TxStatus
- GetResult(principal, tx_id) -> TxResult or TxStatus
```

---

## 22. 测试要求

Codex 实现时必须补充自动化测试。

### 22.1 单元测试

必须覆盖：

1. Key 校验。
2. Content-Type 到 value_type 的映射。
3. JSON 合法性校验。
4. APIKey hash 认证。
5. JWT subject 到 userspace 的映射。
6. Auth cache hit / miss。
7. 事务 seq 重排。
8. 重复片段幂等。
9. seq 冲突进入 aborted。
10. commit 早于 ops 到达。
11. 超时事务 expired。
12. 事务内 GET 读到前序 PUT。
13. 事务失败 rollback。
14. 导入导出 checksum。

### 22.2 集成测试

必须覆盖：

1. 普通 PUT -> GET -> DELETE。
2. 不同用户相同 key 隔离。
3. JWT 与 APIKey 均可访问自身 userspace。
4. 用户 A 不能访问用户 B 的事务。
5. 乱序事务片段提交后按 seq 执行。
6. 重复 commit 返回相同结果。
7. 导出后导入到空 userspace，数据一致。
8. replace / merge-overwrite / merge-skip 三种导入模式。
9. 大二进制 value 存取。
10. 服务重启后已提交数据存在。

### 22.3 并发测试

必须覆盖：

1. 多 goroutine 同时写同一个 key，最终状态必须等价于某个串行顺序。
2. 导入过程中普通请求不能看到半导入状态。
3. 事务提交过程中普通 GET 不能看到事务中间状态。
4. 多个事务并发 commit，结果必须可串行化。

---

## 23. 初始管理能力

为了便于开发和测试，需要提供初始化机制。

最低要求：

1. 启动时如果 system auth store 不存在，可以根据配置创建初始管理员用户。
2. 支持从环境变量注入初始 APIKey。
3. 初始 APIKey 只保存 hash。
4. 初始用户拥有一个 userspace。

建议环境变量：

```text
KVHTTP_BOOTSTRAP_USER_ID=admin
KVHTTP_BOOTSTRAP_USERSPACE_ID=admin_space
KVHTTP_BOOTSTRAP_API_KEY=dev-secret-key
```

生产环境中应提示用户更换初始 APIKey。

---

## 24. 安全要求

1. 不保存 APIKey 明文。
2. 日志不得打印 Authorization Header。
3. 日志不得打印 value。
4. userspace_id 不接受客户端直接传入。
5. tx_id 必须绑定用户身份。
6. 导入数据必须限制大小。
7. 单个 value 必须限制大小。
8. 单个事务操作数必须限制。
9. 事务 body 缓冲必须限制总大小。
10. 所有错误响应不得泄露底层文件路径。
11. HTTP handler 必须设置合理的 request body size limit。
12. 对外默认只开放 `/v1/*`、`/healthz`、`/readyz`、`/metrics`。

---

## 25. Codex 实现顺序建议

请按以下顺序实现，避免一次性生成过多未验证代码。

### 阶段 1：项目骨架

1. 创建 Go module。
2. 创建 cmd / internal 目录结构。
3. 实现配置加载。
4. 实现 HTTP router。
5. 实现统一错误响应。
6. 实现 request_id middleware。

验收：

```text
GET /healthz 返回 200
GET /readyz 返回 200 或明确错误
```

### 阶段 2：存储层

1. 实现 Storage 抽象。
2. 实现 KVRecord 保存与读取。
3. 实现 userspace 隔离。
4. 实现版本号。
5. 实现 checksum。

验收：

```text
PUT / GET / DELETE 可以工作
重启后数据仍存在
```

### 阶段 3：认证层

1. 实现 APIKey 认证。
2. 实现 JWT 认证。
3. 实现 Principal。
4. 实现 Auth Cache。
5. 实现 bootstrap user。

验收：

```text
无认证请求返回 401
APIKey 可以访问自身 userspace
不同用户数据隔离
```

### 阶段 4：普通 KV API

1. PUT。
2. GET。
3. DELETE。
4. HEAD。
5. Content-Type 识别。
6. JSON 校验。
7. 全局锁接入。

验收：

```text
字符串、JSON、二进制均可存取
JSON 非法时返回 422
```

### 阶段 5：事务协调器

1. CreateTx。
2. AddOp。
3. Commit。
4. Abort。
5. GetResult。
6. seq 重排。
7. op 幂等。
8. seq 冲突。
9. timeout cleaner。

验收：

```text
乱序提交 ops 后 commit，按 seq 执行
GET 在事务内按顺序读取
重复片段不产生重复执行
```

### 阶段 6：事务持久化与恢复

1. 事务状态持久化。
2. committed result 持久化。
3. 服务重启恢复状态。
4. 未完成事务重启后按 expired 或 pending 策略处理。

验收：

```text
committed 事务重复查询结果一致
服务重启后 committed 数据仍存在
```

### 阶段 7：导入导出

1. 实现二进制编码。
2. 实现二进制解码。
3. 实现 checksum 校验。
4. 实现 replace。
5. 实现 merge-overwrite。
6. 实现 merge-skip。

验收：

```text
export -> import 后数据一致
错误 checksum 拒绝导入
```

### 阶段 8：可观测性

1. 结构化日志。
2. Prometheus metrics。
3. lock wait 指标。
4. tx 指标。
5. auth cache 指标。

验收：

```text
/metrics 可访问
日志不包含 value 和 Authorization
```

---

## 26. 最终验收标准

项目完成时必须满足：

1. 可以启动一个 HTTP KV 服务。
2. 支持 JWT 和 APIKey 认证。
3. 每个用户有独立 userspace。
4. 支持 string / JSON / binary value。
5. 支持普通 CRUD。
6. 支持多 HTTP 请求聚合事务。
7. 事务支持乱序片段重排。
8. 事务支持幂等重传。
9. 事务支持超时过期。
10. 事务内读操作按 seq 执行。
11. 所有事务通过全局锁 Serializable 执行。
12. 导入导出使用二进制格式。
13. 导入导出具有 checksum 校验。
14. 存储数据进程重启后不丢失。
15. 提供基础日志和 metrics。
16. 自动化测试覆盖核心语义。

---

## 27. 实现时禁止事项

1. 禁止把 userspace_id 从客户端请求参数中直接信任。
2. 禁止把 APIKey 明文写入磁盘。
3. 禁止把 value 写入日志。
4. 禁止事务片段一到达就执行。
5. 禁止 GET 事务片段提前读取数据。
6. 禁止未获得全局锁就执行普通 KV 操作。
7. 禁止导入过程中直接覆盖真实 userspace 且无法 rollback。
8. 禁止在事务失败时保留部分写入。
9. 禁止不同用户复用同一个 tx_id。
10. 禁止把认证数据放进普通 KV userspace。

---
