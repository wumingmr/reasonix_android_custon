import { execFileSync } from "node:child_process";
import { mkdtempSync, copyFileSync, rmSync, readFileSync, mkdirSync, writeFileSync } from "node:fs";
import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { build } from "esbuild";
import { buildBrowserPage } from "./build-browser-page.mjs";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const prepare = process.argv[2] === "--prepare-only" ? process.argv[3] : undefined;
if (process.argv.length > 2 && (!prepare || process.argv.length !== 4)) throw new Error("usage: browser-runtime-smoke.mjs [--prepare-only <directory>]");
const scratch = mkdtempSync(join(tmpdir(), "reasonix-browser-smoke-"));
try {
  await buildBrowserPage();
  for (const name of ["browser-page.js", "browser-page.json", "browser-recorder.js", "recorder-preload.cjs"]) copyFileSync(join(root, "dist", name), join(scratch, name));
  // Top-level await lives inside an async wrapper; the code still uses the
  // production main-process minification and function-name settings.
  const source = readFileSync(join(root, "src/main/browser/browserRuntime.native.ts"), "utf8");
  const imports = source.split("\n").filter(line => line.startsWith("import ")).join("\n");
  const body = source.split("\n").filter(line => !line.startsWith("import ")).join("\n");
  await build({ stdin: { contents: `${imports}\n(async () => {${body}})().then(() => app.quit(), error => { console.error(error); app.exit(1); });`, resolveDir: join(root, "src/main/browser"), loader: "ts" }, outfile: join(scratch, "main.cjs"), bundle: true, platform: "node", format: "cjs", minify: true, keepNames: true, external: ["electron"] });
  const env = { ...process.env };
  await build({ entryPoints: [join(root, "src/main/browser/guestPreload.ts")], outfile: join(scratch, "guest-preload.cjs"), bundle: true, platform: "node", format: "cjs", external: ["electron"] });
  if (prepare) {
    // Stage the identical production-parameter fixture for a native VM. This
    // mode performs no validation; the native runner must produce result.json.
    mkdirSync(prepare, { recursive: true });
    for (const name of ["main.cjs", "guest-preload.cjs", "browser-page.js", "browser-page.json", "browser-recorder.js", "recorder-preload.cjs"]) copyFileSync(join(scratch, name), join(prepare, name));
    console.log(`Native fixture prepared (not executed): ${prepare}`);
  } else {
    delete env.ELECTRON_RUN_AS_NODE;
    // Electron #51488: AppKit may reopen a crash-state modal despite an isolated
    // Chromium profile. Apply the official workaround to this process only.
    const platformArgs = process.platform === "darwin" ? ["-ApplePersistenceIgnoreState", "YES"] : [];
    const recordingSeconds = Number(process.env.REASONIX_BROWSER_RECORD_SECONDS ?? 1);
    if (!Number.isInteger(recordingSeconds) || recordingSeconds < 1 || recordingSeconds > 90) throw new Error("recording test duration must be 1–90 seconds");
    const timeout = Math.max(process.env.REASONIX_BROWSER_CYCLES === "100" ? 180_000 : 60_000, recordingSeconds === 1 ? 0 : (recordingSeconds + 60) * 1000);
    const evidence = process.env.REASONIX_BROWSER_EVIDENCE_PATH || join(root, "artifacts/browser-runtime", process.platform);
    mkdirSync(evidence, { recursive: true });
    // A failed attempt must not leave a previous run's success as its status.
    writeFileSync(join(evidence, "result.json"), JSON.stringify({ completed: false, status: "running", platform: process.platform }));
    try {
      execFileSync(createRequire(import.meta.url)("electron"), [join(scratch, "main.cjs"), ...platformArgs, `--user-data-dir=${join(scratch, "data")}`], { env, stdio: "inherit", timeout, killSignal: "SIGKILL" });
      assert.equal(JSON.parse(readFileSync(join(scratch, "result.json"), "utf8")).completed, true, "native runner exited before completing all assertions");
    } catch (error) {
      writeFileSync(join(evidence, "result.json"), JSON.stringify({ completed: false, status: "failed", platform: process.platform, exitCode: error.status ?? null, signal: error.signal ?? null }));
      throw error;
    }
    for (const name of ["result.json", "browser-evidence.png", "browser-recording.webm", "browser-diagnostics.json"]) copyFileSync(join(scratch, name), join(evidence, name));
    console.log(`Browser runtime evidence: ${evidence}`);
  }
} finally { rmSync(scratch, { recursive: true, force: true }); }
