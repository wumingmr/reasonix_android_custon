import assert from "node:assert/strict";
import { test } from "node:test";
import { readGrantReport, stripPackageGrants, unpaintedWindowCause } from "./packageGrants.js";

const exe = String.raw`C:\Apps\Reasonix\versions\v1.39.1\app\Reasonix.exe`;
const quiet = () => {};

test("package grants are only stripped from a packaged Windows install", () => {
  const never = (): string => assert.fail("the service was run");
  assert.equal(stripPackageGrants("svc", { platform: "darwin", packaged: true, execPath: exe }, quiet, never), null);
  assert.equal(stripPackageGrants("svc", { platform: "linux", packaged: true, execPath: exe }, quiet, never), null);
  assert.equal(stripPackageGrants("svc", { platform: "win32", packaged: false, execPath: exe }, quiet, never), null);

  let called: { binary: string; args: string[] } | undefined;
  const run = (binary: string, args: string[]) => {
    called = { binary, args };
    return JSON.stringify({ stripped: [String.raw`C:\Apps\Reasonix`], refused: [{ path: String.raw`C:\Apps\Reasonix\ffmpeg.dll` }] }) + "\n";
  };
  const report = stripPackageGrants("svc", { platform: "win32", packaged: true, execPath: exe }, quiet, run);
  // The service is told which application, never which directory.
  assert.deepEqual(called, { binary: "svc", args: ["-strip-package-grants", "-app", exe] });
  assert.deepEqual(report, { stripped: [String.raw`C:\Apps\Reasonix`], refused: [String.raw`C:\Apps\Reasonix\ffmpeg.dll`] });
});

test("a grant report that does not parse is no report at all", () => {
  const lines: string[] = [];
  const opts = { platform: "win32" as const, packaged: true, execPath: exe };
  assert.equal(stripPackageGrants("svc", opts, (l) => lines.push(l), () => { throw Object.assign(new Error("exit 2"), { stderr: "strip-package-grants: denied\n" }); }), null);
  assert.equal(stripPackageGrants("svc", opts, (l) => lines.push(l), () => "not json"), null);
  assert.equal(lines[0], "strip-package-grants: strip-package-grants: denied");
  assert.throws(() => readGrantReport(JSON.stringify({ stripped: null, refused: [] })));
  assert.throws(() => readGrantReport(JSON.stringify({ stripped: [] })));
});

test("an unpainted window is attributed only to grants the service could not remove", () => {
  assert.equal(unpaintedWindowCause(null, "en-US"), null);
  assert.equal(unpaintedWindowCause({ stripped: ["C:\\R"], refused: [] }, "en-US"), null);
  const refused = Array.from({ length: 7 }, (_, i) => `C:\\R\\${i}.dll`);
  const en = unpaintedWindowCause({ stripped: [], refused }, "en-US");
  assert.ok(en && en.detail.includes("C:\\R\\4.dll") && !en.detail.includes("C:\\R\\5.dll"));
  assert.ok(en.detail.includes("2 more"));
  const zh = unpaintedWindowCause({ stripped: [], refused: refused.slice(0, 1) }, "zh-CN");
  assert.ok(zh && zh.title.includes("无法打开窗口") && zh.detail.includes("C:\\R\\0.dll"));
});
