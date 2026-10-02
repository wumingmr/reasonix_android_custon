import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, readdirSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import type { HelloResult } from "./handshake.js";
import { ShellLifecycle } from "./shellLifecycle.js";

function hello(enabled: boolean): HelloResult {
  return {
    protocolVersion: 11,
    contractDigest: "sha256:test",
    service: { version: "v1.40.0", channel: "stable", commit: "abc", pid: 42 },
    runtimeGeneration: "g-1",
    runId: "service-run",
    incidentId: "service-incident",
    diagnosticsEnabled: enabled,
    resources: { origin: "http://127.0.0.1:1", token: "token" },
    window: {
      width: 1,
      height: 1,
      minWidth: 1,
      minHeight: 1,
      frameless: false,
      zoomFactor: 1,
    },
  };
}

test("shell lifecycle starts only after diagnostics-enabled hello and removes only its own record", () => {
  const root = mkdtempSync(join(tmpdir(), "reasonix-shell-lifecycle-"));
  const disabled = new ShellLifecycle(root, {
    version: "v1.40.0",
    channel: "stable",
    commit: "abc",
  });
  disabled.start(hello(false));
  const dir = join(root, "diagnostics", "lifecycle");
  assert.deepEqual(readdirSync(root), []);

  const tracker = new ShellLifecycle(root, {
    version: "v1.40.0",
    channel: "stable",
    commit: "abc",
  });
  tracker.start(hello(true));
  const [name] = readdirSync(dir);
  const state = JSON.parse(readFileSync(join(dir, name), "utf8")) as Record<string, unknown>;
  assert.equal(state.schemaVersion, 3);
  assert.equal(state.processRole, "shell");
  assert.equal(state.incidentId, "service-incident");
  tracker.complete();
  assert.deepEqual(readdirSync(dir), []);
});
