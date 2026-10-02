import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { rewriteDragRegions, shellFromEnv } from "./shell-css.mjs";

const stylesPath = resolve(dirname(fileURLToPath(import.meta.url)), "../src/styles.css");

test("browser build keeps the custom property byte-for-byte", () => {
  const css = ".tabbar{--reasonix-draggable:drag}.tabbar *{--reasonix-draggable: no-drag}";
  assert.equal(rewriteDragRegions(css, "browser"), css);
});

test("electron build rewrites every drag declaration", () => {
  const css = ".tabbar{--reasonix-draggable:drag}.tabbar *{--reasonix-draggable: no-drag}/* --reasonix-draggable marks */";
  assert.equal(
    rewriteDragRegions(css, "electron"),
    ".tabbar{-webkit-app-region:drag}.tabbar *{-webkit-app-region: no-drag}/* --reasonix-draggable marks */",
  );
});

test("electron keeps chrome no-drag but does not punch the titlebar with transcript boxes", () => {
  const css = [
    ".topicbar{--reasonix-draggable:drag}",
    ".topicbar button{--reasonix-draggable:no-drag}",
    ".topicbar__title-edit{--reasonix-draggable:no-drag}",
    ".topicbar__title-input{--reasonix-draggable:no-drag}",
    ".msg{--reasonix-draggable:no-drag}",
    ".reasoning__head{-webkit-app-region:no-drag}",
    ".tool__head{-webkit-app-region:no-drag}",
    ".management-screen{--reasonix-draggable:no-drag}",
    ".settings-modal-backdrop{--reasonix-draggable:no-drag}",
    ".workbench-dock__tools{--reasonix-draggable:no-drag}",
  ].join("");
  const out = rewriteDragRegions(css, "electron");
  assert.match(out, /\.topicbar\{-webkit-app-region:\s*drag\}/);
  assert.match(out, /\.topicbar button\{-webkit-app-region:\s*no-drag\}/);
  assert.match(out, /\.topicbar__title-edit\{-webkit-app-region:\s*no-drag\}/);
  assert.match(out, /\.topicbar__title-input\{-webkit-app-region:\s*no-drag\}/);
  assert.match(out, /\.management-screen\{-webkit-app-region:\s*no-drag\}/);
  assert.match(out, /\.settings-modal-backdrop\{-webkit-app-region:\s*no-drag\}/);
  assert.doesNotMatch(out, /\.msg\{[^}]*-webkit-app-region/);
  assert.doesNotMatch(out, /\.reasoning__head\{[^}]*-webkit-app-region/);
  assert.doesNotMatch(out, /\.tool__head\{[^}]*-webkit-app-region/);
  assert.doesNotMatch(out, /\.workbench-dock__tools\{[^}]*-webkit-app-region/);
});

test("chrome selector does not match unrelated topicbar or topbar class names", () => {
  const css = ".topicbarfoo{--reasonix-draggable:no-drag}.topbar2{--reasonix-draggable:no-drag}";
  const out = rewriteDragRegions(css, "electron");
  assert.equal(out, css);
});

test("electron stylesheet rewrite keeps a toast over the titlebar clickable", () => {
  const out = rewriteDragRegions(readFileSync(stylesPath, "utf8"), "electron");
  assert.match(out, /\.toast\s*\{[^}]*-webkit-app-region:\s*no-drag/);
});

test("electron stylesheet rewrite leaves transcript boxes out of app-region", () => {
  const out = rewriteDragRegions(readFileSync(stylesPath, "utf8"), "electron");
  assert.match(out, /\.topicbar\s*\{[^}]*-webkit-app-region:\s*drag/);
  assert.match(out, /\.topicbar button\s*\{[^}]*-webkit-app-region:\s*no-drag/);
  assert.match(out, /\.topicbar__title-edit\s*\{[^}]*-webkit-app-region:\s*no-drag/);
  assert.match(out, /\.topicbar__title-input\s*\{[^}]*-webkit-app-region:\s*no-drag/);
  assert.doesNotMatch(out, /\.msg\s*\{[^}]*-webkit-app-region/);
  assert.doesNotMatch(out, /\.reasoning__head\s*\{[^}]*-webkit-app-region/);
  assert.doesNotMatch(out, /\.tool__head\s*\{[^}]*-webkit-app-region/);
  assert.doesNotMatch(out, /\.process-card__head\s*\{[^}]*-webkit-app-region/);
  assert.doesNotMatch(out, /\.composer-modebar\s*\{[^}]*-webkit-app-region/);
  assert.doesNotMatch(out, /\.transcript__jump-bottom[^{]*\{[^}]*-webkit-app-region/);
});

test("shell selection is explicit", () => {
  assert.equal(shellFromEnv({}), "browser");
  assert.equal(shellFromEnv({ REASONIX_SHELL: "electron" }), "electron");
  assert.throws(() => shellFromEnv({ REASONIX_SHELL: "wails" }), "the Wails shell is retired");
  assert.throws(() => shellFromEnv({ REASONIX_SHELL: "tauri" }));
});
