import assert from "node:assert/strict";
import { test } from "node:test";
import { DiagnosticBuffer } from "./diagnostics.js";

test("diagnostic buffers bound size, redact credentials and retain honest cursors", () => {
  const buffer = new DiagnosticBuffer();
  buffer.add({ kind: "network", message: "Authorization: xyz Bearer abc password=secret https://user:pass@example.test/a?token=private#secret", url: "https://user:pass@example.test/a?token=private#secret", status: 401 });
  const text = JSON.stringify(buffer.read());
  for (const secret of ["xyz", "abc", "private", "user:pass", "=secret"]) assert.equal(text.includes(secret), false);
  buffer.add({ kind: "console", message: 'Cookie: session=hidden-one; secret=hidden-two\n{"password":"hidden-three","token":"hidden-four"}' });
  for (const secret of ["hidden-one", "hidden-two", "hidden-three", "hidden-four"]) assert.equal(JSON.stringify(buffer.read()).includes(secret), false);
  for (let i = 0; i < 300; i++) buffer.add({ kind: "console", message: "x".repeat(4096) });
  const result = buffer.read(1);
  assert.ok(result.entries.length <= 200);
  assert.ok(Buffer.byteLength(JSON.stringify(result.entries)) <= 256 * 1024 + 200);
  assert.equal(result.truncated, true);
  assert.equal(buffer.read().truncated, true, "initial export must also disclose eviction");
  assert.equal(buffer.read(result.cursor).entries.length, 0);
  assert.equal(buffer.read(0, "network").entries.length, 0);
});
