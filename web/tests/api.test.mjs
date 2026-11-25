import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import ts from "typescript";

// Execute the actual client in Node without introducing a separate bundler or
// test dependency. Resolve its local runtime import through an ESM data URL.
function compile(source) {
  return ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 }
  }).outputText;
}
function moduleURL(source) {
  return `data:text/javascript;base64,${Buffer.from(source).toString("base64")}`;
}
const typesURL = moduleURL(compile(await readFile(new URL("../src/lib/types.ts", import.meta.url), "utf8")));
const clientCode = compile(await readFile(new URL("../src/lib/api.ts", import.meta.url), "utf8"))
  .replace('from "./types"', `from "${typesURL}"`);
const { ApiClient } = await import(moduleURL(clientCode));
const session = { apiBaseUrl: "http://localhost:8080", authMode: "ApiKey", credential: "test-key", rememberCredential: false };

for (const [content, expected] of [
  ["backend unavailable", "backend unavailable"],
  [JSON.stringify({ error: "KEY_NOT_FOUND", message: "key not found", request_id: "request-1" }), "key not found"],
  [JSON.stringify({ tx_id: "test", status: "aborted", results: [] }), '{"tx_id":"test","status":"aborted","results":[]}'],
  ["null", "null"]
]) {
  test(`preserves HTTP error without reading the response twice: ${expected}`, async (t) => {
    t.mock.method(globalThis, "fetch", async () => new Response(content, { status: 409 }));
    const client = new ApiClient(session);
    await assert.rejects(client.getKey("key"), (error) => {
      assert.equal(error.status, 409);
      assert.equal(error.message, expected);
      return true;
    });
  });
}

test("transaction headers percent-encode logical keys exactly once", async (t) => {
  let headers;
  t.mock.method(globalThis, "fetch", async (_url, init) => {
    headers = init.headers;
    return new Response('{"tx_id":"test","status":"pending"}', { status: 202 });
  });
  const client = new ApiClient(session);
  await client.addTxOp("test", 1, "PUT", "a%2Fb/100%", "op", "text/plain", "value");
  assert.equal(headers.get("X-KV-Key"), "a%252Fb%2F100%25");
});
