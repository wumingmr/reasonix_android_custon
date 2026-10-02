import assert from "node:assert/strict";
import { test } from "node:test";
import { DEBUGGER_IDLE_MS } from "./debuggerLease.js";
import { crc32, deflateSync } from "node:zlib";
import { FakePage } from "./fakeGuestViews.js";
import { captureScreenshot } from "./screenshot.js";
import { validatePNG } from "./png.js";

function png() {
  const chunk = (kind: string, data: Buffer) => {
    const out = Buffer.alloc(data.length + 12);
    out.writeUInt32BE(data.length); out.write(kind, 4); data.copy(out, 8);
    out.writeUInt32BE(crc32(out.subarray(4, -4)), out.length - 4);
    return out;
  };
  const header = Buffer.alloc(13);
  header.writeUInt32BE(1); header.writeUInt32BE(1, 4); header[8] = 8; header[9] = 6;
  return Buffer.concat([Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]), chunk("IHDR", header), chunk("IDAT", deflateSync(Buffer.from([0, 255, 0, 0, 255]))), chunk("IEND", Buffer.alloc(0))]);
}

test("captures are validated before any file is published", async () => {
  const page = new FakePage(1);
  let writes = 0;
  const deps = { directoryExists: () => true, writeFile: () => { writes++; } };
  const request = { ref: "", fullPage: false, directory: "/scratch" };
  for (const data of [Buffer.alloc(0), Buffer.from("png"), png().subarray(0, 33)]) {
    page.image = { toPNG: () => data, getSize: () => ({ width: 1, height: 1 }) };
    await assert.rejects(captureScreenshot(page, null, 1, request, deps), /invalid_image/);
  }
  page.image = { toPNG: png, getSize: () => ({ width: 0, height: 0 }) };
  await assert.rejects(captureScreenshot(page, null, 1, request, deps), /dimensions/);
  assert.equal(writes, 0);
  page.image = { toPNG: png, getSize: () => ({ width: 1, height: 1 }) };
  assert.equal((await captureScreenshot(page, null, 1, request, deps)).width, 1);
  assert.equal(writes, 1);
});

test("a changed owner cannot publish a captured image", async () => {
  const page = new FakePage(1);
  page.image = { toPNG: png, getSize: () => ({ width: 1, height: 1 }) };
  let checkpoints = 0;
  await assert.rejects(captureScreenshot(page, null, 1, { ref: "", fullPage: false, directory: "/scratch" }, {
    directoryExists: () => true,
    verify: () => { if (++checkpoints === 2) throw new Error("revoked"); },
    writeFile: () => assert.fail("stale image was published"),
  }), /revoked/);
});

test("CRC corruption and excessive full-page geometry fail before delivery", async t => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const corrupt = png(); corrupt[corrupt.length - 1] ^= 1;
  assert.throws(() => validatePNG(corrupt), /invalid_image/);
  const page = new FakePage(1);
  page.debugger.respond = () => ({ cssContentSize: { width: 100000, height: 100000 } });
  await assert.rejects(captureScreenshot(page, null, 1, { ref: "", fullPage: true, directory: "/scratch" }, {
    directoryExists: () => true, writeFile: () => assert.fail("oversize page was written"),
  }), /pixel budget/);
  t.mock.timers.tick(DEBUGGER_IDLE_MS);
  assert.equal(page.debugger.isAttached(), false);
  assert.equal(page.debugger.commands.some((entry) => entry.method === "Page.captureScreenshot"), false);
});
