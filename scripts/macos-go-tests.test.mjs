import assert from "node:assert/strict";
import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import test from "node:test";
import vm from "node:vm";
import { fileURLToPath } from "node:url";
import { darwinRoots, selectPackages, testArgs } from "./macos-go-tests.mjs";

const root = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
const packages = ["reasonix/cmd/reasonix", "reasonix/internal/agent", "reasonix/internal/cli",
  "reasonix/internal/cli/theme", "reasonix/internal/clipboard", "reasonix/internal/sandbox",
  "reasonix/internal/tool/builtin", "reasonix/internal/toolbox", "reasonix/tools/repolint"];

test("the pull-request group keeps darwin packages and drops the rest", () => {
  assert.deepEqual(selectPackages(packages, "darwin"), ["reasonix/internal/cli",
    "reasonix/internal/cli/theme", "reasonix/internal/sandbox", "reasonix/internal/tool/builtin"]);
  // Prefix matching must not swallow a sibling that merely shares a name stem.
  assert.ok(!selectPackages(packages, "darwin").includes("reasonix/internal/clipboard"));
  assert.ok(!selectPackages(packages, "darwin").includes("reasonix/internal/toolbox"));
  assert.deepEqual(selectPackages(packages, "full"), packages);
  assert.deepEqual(testArgs(packages, "darwin").slice(0, 2), ["test", "-timeout=8m"]);
  assert.throws(() => testArgs(packages, "typo"), /Unknown/);
  assert.throws(() => testArgs([], "full"), /Empty/);
});

// The root-module packages whose behaviour only a macOS runner can prove.
// Without this the list silently rots: a new _darwin source would be validated
// on pushes but never on the pull request that introduced it.
test("every darwin-specific package is inside the pull-request group", () => {
  const owners = new Set();
  const walk = dir => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const full = path.join(dir, entry.name);
      if (entry.isDirectory()) {
        if (!["testdata", "node_modules", "third_party", "vendor"].includes(entry.name)) walk(full);
        continue;
      }
      if (!entry.name.endsWith(".go")) continue;
      const darwin = /_darwin(_test)?\.go$/.test(entry.name)
        || /^\/\/go:build[^\n]*\bdarwin\b/m.test(readFileSync(full, "utf8").slice(0, 2048));
      if (darwin) owners.add(`reasonix/${path.relative(root, dir).replaceAll(path.sep, "/")}`);
    }
  };
  for (const top of ["internal", "cmd"]) walk(path.join(root, top));
  assert.ok(owners.size > 0, "expected to find darwin-specific packages");
  const covered = [...owners].filter(pkg => selectPackages([pkg], "darwin").length === 1);
  assert.deepEqual(covered.toSorted(), [...owners].toSorted(),
    "add the uncovered package to darwinRoots in scripts/macos-go-tests.mjs");
  // And the reverse: a root that no longer owns darwin code is dead weight.
  for (const declared of darwinRoots)
    assert.ok([...owners].some(pkg => pkg === declared || pkg.startsWith(`${declared}/`)),
      `${declared} declares darwin coverage but owns no darwin source`);
});

test("CI runs the darwin group on pull requests and the full sweep on pushes", () => {
  const source = readFileSync(path.join(root, ".github/workflows/ci.yml"), "utf8");
  assert.match(source, /run: node scripts\/macos-go-tests\.mjs darwin\n/);
  const releaseControl = source.match(/\n  release-control:\n([\s\S]*?)(?=\n  [a-z][a-z0-9-]*:|$)/)?.[1];
  assert.ok(releaseControl, "release-control job must still exist");
  assert.match(releaseControl, /node --test[\s\S]*?scripts\/macos-go-tests\.test\.mjs/);
  const enabled = (name, event, run) => {
    const step = source.split(`      - name: ${name}\n`)[1]?.split(/\n      - /)[0];
    assert.ok(step, `${name} must still exist`);
    const expression = step.match(/^        if: (.+)$/m)[1];
    return vm.runInNewContext(expression, {
      env: { RUN_STEPS: run }, runner: { os: "macOS" }, github: { event_name: event },
    });
  };
  for (const event of ["pull_request", "push", "workflow_dispatch"]) {
    for (const run of ["true", "false"]) {
      assert.equal(enabled("test", event, run), run === "true" && event !== "pull_request");
      assert.equal(enabled("test (macOS platform packages)", event, run), run === "true" && event === "pull_request");
    }
  }
});
