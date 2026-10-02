import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { createRequire } from "node:module";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { build } from "esbuild";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const temp = mkdtempSync(join(tmpdir(), "reasonix-graphics-smoke-"));
const entry = join(temp, "main.cjs");
const native = process.argv.includes("--native-dialog");
const electron = createRequire(import.meta.url)("electron");
await build({ entryPoints: [join(root, "scripts/fixtures/graphics-recovery-main.ts")], outfile: entry, bundle: true, platform: "node", format: "cjs", external: ["electron"] });
function launch(profile, mode, args = []) {
  const env = { ...process.env, REASONIX_RECOVERY_TEST_HOME: profile, REASONIX_RECOVERY_TEST_MODE: mode, REASONIX_RECOVERY_NATIVE_DIALOG: native ? "1" : "0" };
  delete env.ELECTRON_RUN_AS_NODE;
  execFileSync(electron, [entry, ...args], { env, stdio: "inherit", timeout: native ? 190_000 : 30_000, killSignal: "SIGKILL" });
  return JSON.parse(readFileSync(join(profile, "result.json"), "utf8"));
}
try {
  const profile = join(temp, "gpu");
  const recovery = launch(profile, native ? "native" : "gpu");
  assert.equal(recovery.phase, "completed");
  assert.equal(recovery.relaunchArgs.includes("--reasonix-graphics-recovery"), recovery.actual);
  assert.ok(recovery.events.indexOf("service-shutdown") < recovery.events.indexOf("close-allowed"));
  if (native) console.log("PASS native recovery and draft-loss confirmation dialogs");
  else {
    assert.equal(recovery.prompts.length, 2);
    assert.equal(recovery.prompts[0].buttons[0].includes("compatibility"), recovery.actual);
    assert.match(recovery.prompts[1].title, /Draft not saved/);
    const software = launch(profile, "software", ["--reasonix-graphics-recovery"]);
    assert.equal(software.startup, false); assert.equal(software.actual, false);
    assert.equal(software.argv.includes("--reasonix-graphics-recovery"), false);
    assert.equal(launch(profile, "plain").startup, true);
    console.log("PASS GPU events → confirmed draft loss → ordered restart → actual software rendering → next launch restores preference");
    const renderer = launch(join(temp, "renderer"), "renderer");
    assert.equal(renderer.reloads, 2);
    assert.equal(renderer.prompts[0].buttons[0], "Restart / 重启");
    console.log("PASS real renderer termination reloads once, then presents recovery without misattributing GPU");
    launch(profile, "keep", ["--reasonix-graphics-recovery"]);
    assert.equal(JSON.parse(readFileSync(join(profile, "graphics.json"), "utf8")).hardwareAcceleration, false);
    assert.equal(launch(profile, "plain").startup, false);
    console.log("PASS explicit keep-disabled choice persists across a fresh launch");
    const pendingProfile = join(temp, "pending");
    assert.equal(launch(pendingProfile, "record").prompts.length, 0);
    assert.equal(launch(pendingProfile, "previous").prompts.length, 1);
    assert.equal(launch(pendingProfile, "plain").prompts.length, 0);
    console.log("PASS explicit GPU evidence offers recovery on the next launch and acknowledgement stops repetition");
    for (const mode of ["during-dialog", "during-keep"]) {
      const racingProfile = join(temp, mode);
      launch(racingProfile, mode, mode === "during-keep" ? ["--reasonix-graphics-recovery"] : []);
      assert.equal(JSON.parse(readFileSync(join(racingProfile, "graphics-fault.json"), "utf8")).pending, true);
      assert.equal(launch(racingProfile, "previous").prompts.length, 1);
    }
    console.log("PASS both native notice paths preserve GPU failures that arrive during the dialog");
  }
} finally {
  rmSync(temp, { recursive: true, force: true });
}
