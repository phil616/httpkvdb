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

Do not put raw secrets in keys. Keep key segments bounded. Segment encoding inside a logical key is an application convention; the server sees a flat string. When the logical key already contains `%40`, transport encoding must encode that `%` as `%25`. Do not confuse modeling an encoded segment with encoding the complete key for the HTTP path or X-KV-Key header.

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
| Unique index | Explicit index key plus application coordination across all writers; atomic writes alone do not enforce uniqueness |
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

When splitting lists into buckets, supply a manifest or a deterministic bucket mapping that clients can enumerate without a range scan.

There is no conditional put, compare-and-set, increment, or branching on transaction reads. Strict uniqueness requires an application coordinator covering every participating writer and spanning the check through confirmed commit. Reading the index outside a transaction and later writing it is unsafe without this coordinator. An EXISTS operation only reports presence and never prevents a later PUT. Base and index writes in the same transaction guarantee atomic visibility, not uniqueness by themselves.

The same rule applies to counters, updating JSON lists, and changing an indexed field based on a previous read: concurrent read/compute/write flows can lose updates. A single-process mutex is insufficient when other processes can write the same keys. If a tested conditional-operation extension is required, treat it as a separate server feature request, not an existing capability.

## Write Flows

The flows below require the application coordinator to cover the entire flow, including pre-reads and recovery from an uncertain commit. Otherwise they are only safe with a single writer and do not enforce concurrent invariants.

Create record with unique index:

```text
1. GET index key.
2. If found, reject duplicate.
3. Create transaction with total_ops=2.
4. PUT base record.
5. PUT index record.
6. Commit.
```

Update indexed field (skip index changes when the field is unchanged):

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
6. Back up the target before replacing data. A userspace export covers only KV; stop the server and copy httpkvdb.db when auth and transaction state must also be recoverable. Plan downtime or coordinate all writers so validation counts are meaningful.
7. Write an idempotent migration plan:
   - Read source rows in deterministic order.
   - Transform rows into base KV records.
   - Generate index/reference records.
   - Write through HTTP API or import format, not by mutating storage files directly.
8. Validate:
   - Count source rows versus generated base records.
   - Verify required indexes resolve to existing base records.
   - Sample read paths that the application will use.
   - Export after import for validation/archive; this is not a pre-migration rollback copy.
   - Import assigns new versions and updated_at timestamps; compare content/checksums, not equality of original version numbers.

## What Not To Do

Do not model a relational database literally as one key per cell unless the application truly needs cell-level access.

Do not depend on lexicographic key scans; this project does not expose range-scan APIs.

Do not store application records in the system auth space.

Use transactions for each atomic invariant group or an atomic userspace import for a coordinated bulk restore. Respect per-value, total import, transaction operation-count, and accumulated transaction body limits.
