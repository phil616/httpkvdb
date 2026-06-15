# Modeling And Relational Migration

Use this reference when designing persistence for an app or converting relational schemas into HTTPKVDB key/value records.

## Model From Access Patterns

HTTPKVDB has no SQL, joins, range scans, or built-in secondary indexes. Model for exact-key reads.

Start with:

```text
What exact keys will the application read?
Which values should be written atomically?
Which non-primary-key lookups are required?
Which relationships need application-level integrity checks?
```

Keys are the durable schema. JSON values are the row/document bodies.

## Key Naming

Use versioned, stable, URL-encodable key templates:

```text
app/v1/{entity}/{id}
app/v1/{entity}/{parent_id}/{child_id}
app/v1/index/{entity}/{field}/{encoded_value}
app/v1/ref/{parent_entity}/{parent_id}/{child_entity}
app/v1/meta/{purpose}
```

Examples:

```text
app/v1/users/u_1001
app/v1/orders/o_9001
app/v1/order_items/o_9001/i_1
app/v1/index/users/email/alice%40example.com
app/v1/ref/users/u_1001/orders
```

Do not put raw secrets in keys. Keep key segments bounded. Encode arbitrary external values before placing them in path-like keys.

## Value Design

Use `application/json` for structured records:

```json
{
  "id": "u_1001",
  "email": "alice@example.com",
  "name": "Alice",
  "status": "active",
  "created_at": "2026-06-15T10:00:00Z",
  "updated_at": "2026-06-15T10:00:00Z"
}
```

Prefer aggregate documents when records are normally read and updated together:

```text
orders + order_items -> one order document with embedded items
```

Prefer separate records when children are large, independently updated, or independently deleted:

```text
app/v1/orders/o_9001
app/v1/order_items/o_9001/i_1
app/v1/order_items/o_9001/i_2
```

## Relational-To-KV Mapping

Map relational concepts explicitly:

| Relational concept | HTTPKVDB mapping |
|---|---|
| Table | Key prefix or aggregate type |
| Row | JSON value under one key |
| Primary key | ID segment in the key |
| Composite primary key | Multiple key segments |
| Column | JSON field |
| Unique index | Explicit index key maintained transactionally |
| Non-unique index | Explicit list/set/index records maintained by application |
| Foreign key | Stored target ID plus application validation |
| Join | Multiple exact-key reads or denormalized document |
| Cascade delete | Application transaction deleting base and index keys |

## Example Schema Conversion

Original schema:

```sql
users(id, email unique, name, status)
orders(id, user_id references users(id), status, total)
order_items(order_id, item_id, sku, qty)
```

KV design:

```text
app/v1/users/{user_id}
app/v1/index/users/email/{encoded_email}
app/v1/orders/{order_id}
app/v1/ref/users/{user_id}/orders
```

If order items are read with orders:

```json
{
  "id": "o_9001",
  "user_id": "u_1001",
  "status": "paid",
  "total": 99.5,
  "items": [
    {"item_id": "i_1", "sku": "sku_abc", "qty": 2}
  ]
}
```

If order items need independent lifecycle:

```text
app/v1/orders/o_9001
app/v1/order_items/o_9001/i_1
app/v1/order_items/o_9001/i_2
```

## Index Records

HTTPKVDB does not maintain indexes automatically. Create extra records for every required lookup.

Unique lookup:

```text
app/v1/index/users/email/alice%40example.com
-> {"user_id":"u_1001"}
```

Parent list lookup:

```text
app/v1/ref/users/u_1001/orders
-> {"order_ids":["o_9001","o_9002"]}
```

For high-churn lists, split list buckets to avoid repeatedly rewriting very large JSON arrays:

```text
app/v1/ref/users/u_1001/orders/2026-06
```

Because there is no conditional put in the current project, strict uniqueness is an application protocol: read the index, decide, then write base record and index in a transaction. If concurrent creators can race for the same unique key, add an application-level coordinator or extend HTTPKVDB with tested conditional operations before claiming hard uniqueness.

## Write Flows

Create record with unique index:

```text
1. GET index key.
2. If found, reject duplicate.
3. Create transaction with total_ops=2.
4. PUT base record.
5. PUT index record.
6. Commit.
```

Update indexed field:

```text
1. GET current base record.
2. GET new index key; reject if it points to a different record.
3. Transaction:
   - PUT updated base record.
   - DELETE old index key.
   - PUT new index key.
4. Commit.
```

Delete record:

```text
1. GET current base record to discover index keys and references.
2. Transaction:
   - DELETE base record.
   - DELETE unique index keys.
   - Update or DELETE parent reference/list keys.
   - DELETE children only if cascade semantics are required.
3. Commit.
```

## Migration Methodology

1. Inventory schema and access patterns. Do not blindly convert every SQL index.
2. Choose aggregate boundaries: embed child rows that are always read with the parent; split independent or large children.
3. Define key templates and JSON schemas for each aggregate.
4. Define required index and reference keys.
5. Define transaction boundaries for create, update, delete, and migration batches.
6. Write an idempotent migration plan:
   - Read source rows in deterministic order.
   - Transform rows into base KV records.
   - Generate index/reference records.
   - Write through HTTP API or import format, not by mutating storage files directly.
7. Validate:
   - Count source rows versus generated base records.
   - Verify required indexes resolve to existing base records.
   - Sample read paths that the application will use.
   - Export after import for rollback/archive.

## What Not To Do

Do not model a relational database literally as one key per cell unless the application truly needs cell-level access.

Do not depend on lexicographic key scans; this project does not expose range-scan APIs.

Do not store application records in the system auth space.

Do not bypass the transaction API for multi-key invariants.
