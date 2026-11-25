# KV over HTTP 单机强一致存储数据库技术规格说明书

版本：v0.1（与当前实现同步）

目标读者：Codex / Go 开发者 / 项目维护者  
实现语言：Go  
系统类型：单机 HTTP KV 数据库  
核心目标：通过 HTTP 操作字符串 Key 与多类型 Value，并在单机环境下提供强一致事务能力。

本文件规定服务行为。标记“建议”的目录、接口抽象和开发阶段仅用于设计参考，不代表额外功能已经实现。实际存储布局见 6.2，线协议见 11、16.3，实际配置名和默认值见 19。客户端接入说明见 [GUIDE.md](../GUIDE.md)，完整环境模板见 [configs/kvhttpd.env.example](../configs/kvhttpd.env.example)。

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

服务端内部不得直接把 key 当作文件路径使用。当前实现将 Key 作为 bbolt bucket 中的键保存。

HTTP 路径中的完整 Key 必须只 percent-encode 一次，服务端只解码一次；`a/b`、字面量 `a%2Fb`、`100%` 分别编码为 `a%2Fb`、`a%252Fb`、`100%25`。不使用表单编码，`+` 是字面量加号。路由器不会清理 Key 中的点段或重复斜杠。客户端或代理若会规范化 `.`、`..` 路径，应改用事务的 `X-KV-Key` Header 操作这些 Key。Key 没有目录、前缀查询或文件扩展名语义。

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

省略 Content-Type 时使用 `application/octet-stream`。Content-Type 最长 65535 字节（受导出格式 uint16 长度限制），不得包含 CR、LF 或 NUL；非法元信息返回 `400 BAD_REQUEST`。空 Value 可以通过普通 PUT 保存；事务 PUT 片段要求非空 body。

---

## 5. Userspace 模型

### 5.1 基本规则

每个认证 Principal 对应一个 userspace，隔离边界是 userspace_id。多个凭据可以映射到同一 userspace；用户 ID 字符串相同本身不会授予其他 userspace 的权限。

相同 key 在不同 userspace 中互不影响：

```text
userspace_a: key1 -> value_a
userspace_b: key1 -> value_b
```

普通 KV 请求不能由客户端任意指定授权 userspace。userspace 必须由认证结果解析得到。

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

用户空间 URL 形态 `/api/v1/{userspace_id}/{key}` 允许客户端把 userspace 放在路径中，但该路径值只用于路由和可读性。服务端必须把它与认证结果中的 `Principal.userspace_id` 做一致性校验，校验失败返回 `403 FORBIDDEN`，不得把 URL 中的 userspace 作为授权来源。

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

### 6.2 当前存储布局

```text
<KVHTTP_STORAGE_PATH>/httpkvdb.db
```

使用 bbolt v1.3.11，数据库以 0600 权限创建，新数据目录以 0700 权限创建。现有目录的权限不会自动修改。文件持有独占锁；第二个实例在约 1 秒后打开失败，不允许多进程同时写同一个数据库。相对路径以进程工作目录为基准，在打开时固定为绝对路径。

实际 bucket：

```text
meta                      # schema、全局版本序号、bootstrap 标记
kv                        # 每个 userspace 对应一个嵌套 bucket
system:api_keys            # HMAC-SHA256 APIKey 摘要与 Principal
system:jwt_subjects        # JWT subject 与 Principal
tx:states                 # 事务片段、状态与结果
```

写入使用默认开启磁盘同步的原生事务，KV 更新与事务最终结果同一次提交。业务访问仍受全局串行化锁约束；不向客户端提供 MVCC 接口。

没有存储 schema 标记时，若存在旧 `httpkvdb.json`，在一个原子事务内迁移全部 KV、认证、版本和事务状态。空、截断或缺少必需数据区的旧文件会导致启动失败；已有原生数据库缺少必需 bucket 或 schema 不可识别时也会拒绝启动，不自动补空数据区。迁移完成后以 `httpkvdb.db` 为唯一数据源，旧 JSON 和 `userspaces/` 镜像保留但不再读写。存在带 schema 标记的新数据库时，不会根据旧镜像恢复或覆盖新数据。旧版的重复 URL 解码别名不再成立；迁移保留已存储的逻辑 Key，不会自动改名，客户端应移除针对旧路由的二次编码补丁。

升级前停服备份整个目录；当前数据库的完整备份需停服后复制 `httpkvdb.db`。`/v1/export` 仅导出当前 userspace 的 KV，不能替代包含认证和事务状态的完整备份。

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

普通 API 路径前缀（用户空间 KV 另使用 `/api/v1/{userspace_id}/{key}`）：

```text
/v1
```

所有需要身份的 API 必须通过认证中间件。

---

## 8. 普通 KV API

普通 KV API 视为单操作事务，也必须进入全局锁。

兼容路径：

```text
/v1/kv/{key}
```

用户空间路径：

```text
/api/v1/{userspace_id}/{key}
```

两种路径执行相同 KV 语义。`/api/v1/{userspace_id}/{key}` 必须先确认 `{userspace_id}` 与认证 Principal 的 `userspace_id` 一致。

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

删除采用严格语义：不存在返回 `404 KEY_NOT_FOUND`。当前没有幂等删除配置。

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
- finished_at: timestamp（进入终态时设置，旧记录可能没有）
- commit_received: bool
- tx_digest: string optional
- ops: map[int]TxOperation
- result: TxResult optional
- abort_reason: string optional
```

ready / committing 为保留的内部枚举；当前对外或持久化的状态为 pending、waiting_for_ops、committed、aborted、expired。

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
3. `timeout_ms` 省略或为 0 时使用默认超时，负值非法，不得超过配置上限，默认最大 300000。
4. 如果客户端提供 `tx_id`，服务端校验格式；已存在的同 owner / total_ops 请求视为创建重试，返回原事务，其他复用返回 `400 INVALID_TX`。
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

成功返回 `202 Accepted`。片段尚未触发提交时，响应示例：

```json
{
  "tx_id": "tx_abc123",
  "status": "pending",
  "total_ops": 3,
  "deadline": "2026-04-29T12:00:30Z",
  "received_seq": [1],
  "missing_seq": [2, 3]
}
```

若 commit 已到达，最后一个片段会触发执行；该片段请求仍返回 `202`，body 为 committed `TxResult`。客户端应检查 `status`，不能仅凭 `202` 判断事务仍在等待。

规则：

1. `seq` 从 1 开始。
2. `seq` 必须小于等于 `total_ops`。
3. `X-KV-Op` 必须是 `GET`、`PUT`、`DELETE`、`EXISTS`、`HEAD` 之一。
4. `X-KV-Key` 必须将完整 Key percent-encode 一次；服务端解码一次后校验 UTF-8 和长度。不要用 `+` 编码空格。
5. `X-KV-Op-Id` 用于幂等重试，不能为空。
6. PUT 必须携带非空 body，空 body 返回 `400 BAD_REQUEST`。
7. GET / DELETE / EXISTS / HEAD 不得携带 body，包括未知长度或 chunked body，否则返回 `400 BAD_REQUEST`。
8. JSON 类型 PUT 必须校验 JSON 合法性。
9. 片段提交后不得立即执行。
10. 片段必须持久化到事务状态存储，避免进程重启后事务状态完全丢失。
11. 每个 PUT 片段受 `KVHTTP_MAX_VALUE_SIZE` 限制；同一事务所有片段 body 的累计字节数受 `KVHTTP_MAX_TX_BODY_SIZE` 限制，超出返回 `413 VALUE_TOO_LARGE`，不会接收该片段。相同片段重试不会重复计入大小。

### 11.3 事务片段冲突处理

如果同一个 `tx_id + seq` 重复提交，服务端应比较：

```text
op_id
op_type
key
content_type
body_hash
```

如果完全一致且事务未中止或过期，返回 202，视为幂等重传。已经 committed 的事务也返回原始 committed 结果，不重新执行。

如果不一致且事务尚未终结，事务进入 aborted，返回以下错误；已经 committed 的事务保持 committed 并返回 `409 TX_ALREADY_COMMITTED`。aborted / expired 事务分别返回 `409 TX_ABORTED` / `410 TX_EXPIRED`，不会恢复为可写状态：

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
    },
    {
      "seq": 3,
      "op": "HEAD",
      "status": 200,
      "key": "profile",
      "content_type": "application/json",
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
2. 等待缺失片段不是错误。事务终态、身份、参数、摘要或存储错误仍可能使 commit 失败。
3. 一旦 commit_received=true，事务等待缺失片段直到 deadline。
4. 所有片段到齐后，服务端应自动执行事务。
5. 执行结果可以通过本次 commit 响应返回，也可以之后通过 result API 查询。
6. 空 body 或 `{}` 表示使用已有 total_ops 且不新增摘要。非空控制 body 必须是单个 JSON 对象，非法 JSON、null、数组或尾随内容返回 `400 BAD_REQUEST`。创建事务、commit、创建 userspace 的 JSON body 上限均为 1 MiB，超限返回 `413 VALUE_TOO_LARGE`；未识别的 JSON 字段被忽略。
7. `total_ops` 省略或为 0 时沿用创建值，其他值必须匹配。等待期间已指定的非空 `tx_digest` 不会被空重试清除；重试携带不同非空摘要返回 `400 INVALID_TX`。已提交事务的 commit 始终返回原结果。
8. 当前摘要为兼容格式：按 seq 排序，逐条拼接 `<seq>\0<op_id>\0<op_type>\0<解码后的 key>\0<body_hash>\n` 的 UTF-8 字节，再计算 `sha256:<小写 hex>`。`\0` 为 NUL 字节，`\n` 为换行字节；body_hash 为原始 body 的 SHA-256（无 body 时散列空字节）。此摘要不覆盖 Content-Type，不能当作完整操作元信息的签名；身份认证仍必须独立完成。

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

已经 committed 的事务不能 abort，返回 `409 TX_ALREADY_COMMITTED`；expired 返回 `410 TX_EXPIRED`；重复 abort 已 aborted 的事务返回同样的 200 状态。

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
8. 将 tx result、finished_at 与 committed 状态写入同一个底层事务
9. 一次原子提交 KV 与 tx result，完成持久化确认
10. 释放 Global Serializable Lock
11. 发送响应（慢客户端不会改变已提交状态）
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

GET / HEAD / DELETE 读取或删除不存在的 Key 会回滚整个事务，HTTP 返回 `409`，body 为带失败项的 aborted `TxResult`。EXISTS 的不存在结果仅为该项的 `404 KEY_NOT_FOUND`，不会中止事务。EXISTS 不是条件写入或断言；事务不支持基于读结果动态执行分支。事务之外的“先读、计算、再写”不受同一次全局锁保护；严格唯一性、计数增量或列表读改写需要应用侧协调所有写入者，不能仅凭 Serializable 宣称它们无竞争。

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
4. 终态进入时间超过 `tx_result_retention_ms` 后删除事务片段 body；expired 的终态时间取 deadline，旧记录没有 finished_at 时以 deadline 为基准。
5. 保留 op_id、Key、body_hash 等片段身份信息和完整最终结果，以支持已提交操作的重试。该配置不删除事务记录或结果；GET 结果中的 base64 Value 仍可能占空间。pending / waiting_for_ops 在未过期时不清理 body。
6. 清理释放可复用的数据库页，不保证缩小磁盘文件；当前没有自动压缩或终态记录删除接口。清理失败会记录不含内容的 `tx_cleanup_error` 事件，并在下个周期重试。

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

为了兼容 userspace URL API，也支持直接 APIKey Header：

```http
APIKey: <api_key>
X-API-Key: <api_key>
```

### 15.2 JWT 验证

JWT 验证流程：

1. 解析唯一的认证 Header（见 15.3）。
2. 验证 Bearer token 格式。
3. 校验 Header 的 `alg=HS256` 并验证 HMAC-SHA256 签名；不支持其他算法、非空 crit 扩展或 JWKS/kid 选钥。
4. 要求非空 sub 和有效的 exp；exp 必填且必须晚于当前时间。nbf 若提供则校验，iss / aud 仅在配置了对应预期值时校验；aud 支持字符串或字符串数组。
5. 提取 subject。
6. 从系统认证空间映射到 user_id 和 userspace_id。
7. 生成 Principal。

### 15.3 APIKey 验证

APIKey 验证流程：

1. 解析 `APIKey`、`X-API-Key` 或 `Authorization` Header。
2. 提取 APIKey 明文。
3. 计算 hash。
4. 查询系统认证空间中的 api_key_hash。
5. 校验 key 状态。
6. 映射到 user_id 和 userspace_id。
7. 生成 Principal。

系统不得保存 APIKey 明文。实际摘要为 `hmac-sha256:<hex>`，pepper 来自 `KVHTTP_API_KEY_PEPPER`，为空时使用 JWTSecret。

每个请求只允许一种非空凭据来源：APIKey、X-API-Key 或 Authorization。重复的同名认证 Header 或多种来源同时出现均返回 `401 UNAUTHORIZED`，不会按优先级选择。Authorization 的 ApiKey / Bearer scheme 不区分大小写。服务不提供登录、签发 JWT 或用户自注册接口；JWT subject 需要预先映射到系统认证区，创建 userspace 的 HTTP 接口只生成 APIKey。

### 15.4 Auth Cache

为了避免每个请求都读硬盘，必须实现认证缓存。

实际缓存对象：

```text
api_key_hash -> Principal
jwt_subject -> Principal
```

每次 Bearer 请求仍会校验签名和时效，再使用 subject 缓存。认证管理操作会主动失效缓存，失效期间的旧查询不能重新填回过期 Principal。

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
8. 整个导入体受 `KVHTTP_MAX_IMPORT_SIZE` 限制，每条 Value 仍受 `KVHTTP_MAX_VALUE_SIZE` 限制；两者包括的对象不同。导入导出目前整包缓冲在内存中，不是流式操作；导出本身没有总大小上限，超出导入上限的导出需要调大上限才能重导入。
9. 导入默认 replace；每条实际写入分配新的全局版本和 updated_at，保留非零 created_at，重算 checksum / value_type。导入是内容恢复，不恢复原版本序号或覆盖其他 userspace。
10. 导出在全局锁内复制一致快照，随后在锁外编码并发送；导入在锁外完成解析，在全局锁内原子应用。

### 16.3 二进制格式

实际格式（version=1，无可选 header checksum）：

```text
Header
- magic: 8 bytes, fixed: KVHTTP01
- format_version: uint32
- created_at_unix_ms: int64
- record_count: uint64

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

所有整数固定使用 Big Endian。value_type 编码为 1=string、2=json、3=binary，其他值和重复 Key 均为非法格式。每条 checksum 是 value 原始字节的 SHA-256；footer 是其之前全部字节的 SHA-256。时间戳单位为 Unix 毫秒。Content-Type 使用 uint16 长度，最多 65535 字节。

---

## 17. 可观测性

### 17.1 日志

日志必须避免记录 value 内容。

当前记录服务启动、停止、启动失败及 `tx_cleanup_error`。没有逐请求访问日志、认证审计日志或事务事件日志；不得记录 value、APIKey、JWT 或 Authorization。新增日志时同样遵守这些限制。

### 17.2 Metrics

提供 Prometheus 风格指标接口：

```http
GET /metrics
```

实际提供的无标签计数器：

```text
http_requests_total
kv_get_total
kv_put_total
kv_delete_total
tx_created_total
tx_committed_total
tx_aborted_total
tx_expired_total
import_total
export_total
```

事务计数器按成功持久化的状态变化累计，重试不重复计数，最终片段触发的自动提交也计入。kv_get_total 包含普通 GET 与 HEAD。http_requests_total 包含探针和指标请求，CORS OPTIONS 在该计数层之前处理。计数器仅在进程内保存，重启归零。目前没有延迟、锁等待、认证缓存命中指标或 labels。

### 17.3 Health Check

```http
GET /healthz
```

只表示进程存活。

```http
GET /readyz
```

检查数据库是否可以开启只读事务、schema 是否可识别、KV / 认证 / 事务 bucket 是否齐全。失败返回 `503 STORAGE_ERROR`。此探针不执行落盘写测试，也不检查磁盘剩余空间或后台清理 goroutine 的健康状态。GET 返回 `ok\n`；HEAD 仅返回相同状态与响应头。

---

## 18. 错误格式

除事务操作失败的 aborted `TxResult`（见 12.3）外，应用错误响应使用统一格式；HEAD 只发送状态和响应头：

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
| 400 | `INVALID_USERSPACE` | userspace 不合法 |
| 400 | `INVALID_TX` | 事务参数不合法 |
| 401 | `UNAUTHORIZED` | 未认证或认证失败 |
| 403 | `FORBIDDEN` | 无权限 |
| 404 | `KEY_NOT_FOUND` | key 不存在 |
| 404 | `TX_NOT_FOUND` | 事务不存在 |
| 404 | `USERSPACE_NOT_FOUND` | 管理接口的 userspace 不存在 |
| 404 | `NOT_FOUND` | 路由或该方法对应的端点不存在 |
| 405 | `METHOD_NOT_ALLOWED` | KV 端点不支持该方法 |
| 409 | `USERSPACE_EXISTS` | userspace 已存在 |
| 409 | `SEQ_CONFLICT` | 同一事务序号内容冲突 |
| 409 | `TX_ALREADY_COMMITTED` | 事务已提交 |
| 409 | `TX_ABORTED` | 事务已中止 |
| 410 | `TX_EXPIRED` | 事务已过期 |
| 413 | `VALUE_TOO_LARGE` | 请求体或事务累计 body 超限 |
| 422 | `INVALID_JSON` | JSON Value 不合法 |
| 500 | `STORAGE_ERROR` | 存储错误 |
| 503 | `STORAGE_ERROR` | 就绪检查失败 |

---

## 19. 配置项

服务必须支持配置文件或环境变量。

未指定 `--config` 时仅加载进程环境中 `KVHTTP_` 开头的变量；指定 `--config <path>` 时只读取该文件，不叠加进程环境。缺失项和空值使用默认值。文件使用 `KEY=value`，支持空行、整行 `#` 注释、`export ` 前缀、单引号字面量和双引号 Go 转义；不是 shell 脚本，不展开变量或命令，不支持行内注释。未知配置名、重复文件配置名、非法引号和非十进制数值导致启动失败，不回显配置值。

| 配置名 | 默认值 | 约束 / 含义 |
|---|---|---|
| `KVHTTP_ADDR` | `0.0.0.0:8080` | HTTP 监听地址 |
| `KVHTTP_STORAGE_PATH` | `./data` | 数据目录，非数据库文件名 |
| `KVHTTP_CORS_ALLOWED_ORIGINS` | `http://127.0.0.1:5173,http://localhost:5173` | 逗号分隔 Origin；`*` 表示任意 Origin |
| `KVHTTP_MAX_KEY_SIZE` | `4096` | 1–32768 字节，后端键大小限制 |
| `KVHTTP_MAX_VALUE_SIZE` | `104857600` | 单 Value / 单 PUT 片段的字节上限 |
| `KVHTTP_MAX_IMPORT_SIZE` | `1073741824` | 整个导入体的字节上限 |
| `KVHTTP_MAX_TX_OPS` | `1000` | 单事务操作数上限 |
| `KVHTTP_MAX_TX_BODY_SIZE` | `268435456` | 单事务累计 body 字节上限 |
| `KVHTTP_DEFAULT_TX_TIMEOUT_MS` | `30000` | 不得大于 max timeout |
| `KVHTTP_MAX_TX_TIMEOUT_MS` | `300000` | 单事务最大等待毫秒数 |
| `KVHTTP_TX_CLEAN_INTERVAL_MS` | `5000` | 过期和 body 清理周期 |
| `KVHTTP_TX_RESULT_RETENTION_MS` | `3600000` | 终态片段 body 保留期；不删除结果 |
| `KVHTTP_AUTH_CACHE_TTL_MS` | `60000` | Principal 缓存 TTL |
| `KVHTTP_AUTH_CACHE_MAX_ENTRIES` | `10000` | Principal 缓存条目上限 |
| `KVHTTP_BOOTSTRAP_USER_ID` | `admin` | 首次初始化的用户 ID |
| `KVHTTP_BOOTSTRAP_USERSPACE_ID` | `admin_space` | 首次初始化的 userspace |
| `KVHTTP_BOOTSTRAP_API_KEY` | `dev-secret-key` | 仅适合本地开发的默认 Key |
| `KVHTTP_API_KEY_PEPPER` | 空 | 为空时使用 JWTSecret；应单独设置 |
| `KVHTTP_JWT_SECRET` | `dev-jwt-secret` | HS256 密钥，生产必须替换 |
| `KVHTTP_JWT_ISSUER` | 空 | 非空时校验 iss |
| `KVHTTP_JWT_AUDIENCE` | 空 | 非空时校验 aud |

所有数值限制必须为正，毫秒数还必须能安全转换成 time.Duration。服务没有 `log.level`、`metrics.enabled`、幂等删除等配置。限制作用于单个请求或事务；没有全局活动事务数量或缓冲总额限制，应结合部署资源控制负载。

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
- CreateUserspace(userspace_id, user_id, api_key_hash) -> error
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
15. userspace_id 校验。

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
11. `/api/v1/{userspace}/{key}` 匹配认证 userspace 时可访问。
12. `/api/v1/{userspace}/{key}` 不匹配认证 userspace 时返回 403。
13. 管理员创建 userspace 后生成可用 APIKey。
14. 重复创建同一 userspace 返回冲突。

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
4. 初始用户拥有一个 userspace。bootstrap 只在无认证记录且未标记初始化时创建；之后不会恢复已删除或轮换的 Key。
5. 管理员可以创建新的 userspace，并为其生成 APIKey。

建议环境变量：

```text
KVHTTP_BOOTSTRAP_USER_ID=admin
KVHTTP_BOOTSTRAP_USERSPACE_ID=admin_space
KVHTTP_BOOTSTRAP_API_KEY=dev-secret-key
```

生产环境中应提示用户更换初始 APIKey。

管理员创建 userspace API：

```http
POST /v1/admin/userspaces
APIKey: <admin_api_key>
Content-Type: application/json

{
  "userspace_id": "alice",
  "user_id": "alice"
}
```

成功返回：

```json
{
  "user_id": "alice",
  "userspace_id": "alice",
  "api_key": "generated-plaintext-api-key"
}
```

`api_key` 明文只允许在创建响应中返回一次，磁盘中只能保存其 HMAC-SHA256 摘要。重复创建同一 userspace 必须返回 `409 USERSPACE_EXISTS`。

userspace_id 必须匹配 `[A-Za-z0-9][A-Za-z0-9._-]{0,127}`；未指定 user_id 时使用 userspace_id。创建 userspace 只发放 APIKey，不创建 JWT subject。

管理员 userspace 管理 API：

```text
GET    /v1/admin/userspaces
DELETE /v1/admin/userspaces/{userspace_id}
POST   /v1/admin/userspaces/{userspace_id}/api-key
GET    /v1/admin/userspaces/{userspace_id}/keys
PUT    /v1/admin/userspaces/{userspace_id}/kv/{key}
GET    /v1/admin/userspaces/{userspace_id}/kv/{key}
HEAD   /v1/admin/userspaces/{userspace_id}/kv/{key}
DELETE /v1/admin/userspaces/{userspace_id}/kv/{key}
```

以上接口必须要求 `admin` 角色。管理员 KV 操作用于颗粒化运维管理，必须和普通 CRUD 一样经过全局串行化锁，不得记录原始 value。APIKey 轮换只允许在响应中返回新明文一次，使该 userspace 的旧 APIKey 失效并保留 Principal 的角色。删除 userspace 会原子删除其 KV、APIKey、JWT subject 和事务状态，且不会被下次 bootstrap 重建。

---

## 24. 安全要求

1. 不保存 APIKey 明文。
2. 日志不得打印 Authorization Header。
3. 日志不得打印 value。
4. userspace_id 不得直接信任客户端传入值；如果 URL 中包含 userspace_id，必须与 Principal.userspace_id 校验一致。
5. tx_id 必须绑定用户身份。
6. 导入数据必须限制大小。
7. 单个 value 必须限制大小。
8. 单个事务操作数必须限制。
9. 事务 body 缓冲必须限制总大小。
10. 所有错误响应不得泄露底层文件路径。
11. HTTP handler 必须设置合理的 request body size limit。
12. 对外默认只开放 `/v1/*`、`/api/v1/*`、`/healthz`、`/readyz`、`/metrics`。

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
