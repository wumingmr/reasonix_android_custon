import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { startupPresentation } from "./startupPresentation.js";

test("a healthy first boot stays hidden", () => {
  assert.equal(startupPresentation({ serviceReady: false, hasWindow: false, lifecycle: "starting" }), "none");
});

test("a second click while starting does not open the wait page", () => {
  assert.equal(startupPresentation({ serviceReady: false, hasWindow: false, lifecycle: "starting" }), "none");
});

test("a second click focuses an existing window instead of replacing it", () => {
  assert.equal(startupPresentation({ serviceReady: false, hasWindow: true, lifecycle: "starting" }), "focus");
  assert.equal(startupPresentation({ serviceReady: true, hasWindow: true, lifecycle: "ready" }), "focus");
});

test("a failed or timed-out startup still shows the recovery page", () => {
  assert.equal(startupPresentation({ serviceReady: false, hasWindow: false, lifecycle: "failed" }), "diagnostic");
  assert.equal(startupPresentation({ serviceReady: false, hasWindow: true, lifecycle: "failed" }), "diagnostic");
});

test("whenReady does not create or show a provisional diagnostic window", () => {
  const source = readFileSync(fileURLToPath(new URL("./index.ts", import.meta.url)), "utf8");
  const end = source.indexOf("return service.start()");
  const start = source.lastIndexOf("void app.whenReady()", end);
  assert.ok(start >= 0 && end > start, "index.ts must still start the service from whenReady");
  const boot = source.slice(start, end);
  assert.equal(boot.includes("showFailure"), false, "first boot must not show the diagnostic wait page");
  assert.equal(boot.includes("mainWindow.create("), false, "first boot must not create a window just to flash a wait page");
  assert.equal(source.includes('status.lifecycle === "starting" ? startingPage'), false, "second clicks must not load the wait page while still starting");
});
