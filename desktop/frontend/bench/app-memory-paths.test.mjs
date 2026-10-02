import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { memoryAffected, memoryMode } from "./app-memory-paths.mjs";

const benchDir = path.dirname(fileURLToPath(import.meta.url));
test("all frontend consumers, configuration and tests trigger the soak", () => {
  for (const path of ["desktop/frontend/src/App.tsx", "desktop/frontend/src/lib/types.ts", "desktop/frontend/pnpm-lock.yaml", "desktop/frontend/bench/fixture.json", ".github/workflows/app-memory.yml"])
    assert.equal(memoryAffected([path]), true, path);
});
test("known independent backend and documentation changes skip only this mock frontend soak", () => {
  assert.equal(memoryAffected(["internal/control/turn.go", "desktop/app.go", "sdk/types.ts", "docs/guide.md", "README.md", "desktop/AGENTS.md"]), false);
});
test("repository tooling, workflows and the Electron shell do not reach the browser-mocked bundle", () => {
  for (const path of [".github/actions/go-build-cache/action.yml", "scripts/release-stable.sh", "tools/desktopinventory/sources.go",
    "workers/crash-report/src/index.ts", "docs/desktop-migration/inventory.json", "desktop/electron/src/main/dialogs.ts", "desktop/packaging/smoke.mjs",
    "desktop/build/windows/installer/project.nsi", "desktop/go.sum", "go.mod", ".golangci.yml", "Makefile"])
    assert.equal(memoryAffected([path]), false, path);
});
test("unknown paths fail closed and cannot be hidden by a documentation change", () => {
  for (const path of [".npmrc", "shared/new-loader.js", "desktop/package.json", "desktop/pnpm-lock.yaml", "desktop/frontend/scripts/shell-css.mjs"])
    assert.equal(memoryAffected(["README.md", path]), true, path);
});
test("ordinary frontend changes use the short screen and lifecycle changes use the full screen", () => {
  assert.equal(memoryMode(["desktop/frontend/src/components/Button.tsx"]), "short");
  assert.equal(memoryMode(["desktop/frontend/src/App.tsx"]), "full");
  assert.equal(memoryMode(["desktop/frontend/src/lib/subscriptionScope.ts"]), "full");
  assert.equal(memoryMode(["docs/guide.md"]), "off");
  assert.equal(memoryMode(["shared/new-loader.js"]), "full");
});

test("local session benchmarks navigate through the shared layout-independent contract", () => {
  for (const name of ["app-memory.mjs", "app-browser.mjs", "composer-transcript-stability.mjs", "dock-view-state.mjs", "run.mjs", "runtime-state.mjs"]) {
    const source = readFileSync(path.join(benchDir, name), "utf8");
    assert.doesNotMatch(source, /project-tree__topic-main:has-text\(["'`]bench:/, name);
  }
});
