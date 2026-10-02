import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { conptyProbe, groups, historyTests, inventoryFromJSON, owners, testArgs, verifyInventory } from "./desktop-windows-go-tests.mjs";

test("every test prefix, example and fuzz seed has exactly one execution owner", () => {
  const names = [..."ABCDEFGHIJKLMNOPQRSTUVWXYZ"].map(letter => `Test${letter}Feature`);
  names.push("Test", "Test_Compatibility", "Test中文", "Example", "ExampleController_Open", "FuzzSession", conptyProbe, ...Object.values(historyTests));
  for (const name of names) assert.equal(owners(name).length, 1, name);
  assert.deepEqual(owners("ExampleController_Open"), ["A-B"]);
  assert.deepEqual(owners("FuzzSession"), ["A-B"]);
  assert.deepEqual(owners(conptyProbe), ["conpty-probe"]);
  const counts = verifyInventory(new Map(names.map(name => [name, name])));
  assert.equal(Object.values(counts).reduce((sum, count) => sum + count, 0), names.length);
  assert.throws(() => testArgs("missing"), /Unknown/);
  assert.throws(() => verifyInventory(new Map()), /Empty/);
});

test("all historical releases have an independent bounded execution owner", () => {
  for (const [group, name] of Object.entries(historyTests)) {
    assert.deepEqual(owners(name), [group]);
    assert.deepEqual(testArgs(group, true), ["test", "-race", "-timeout=25m", "-run", `^${name}$`, "./..."]);
  }
  const source = readFileSync(new URL("../.github/workflows/ci.yml", import.meta.url), "utf8");
  const ordinary = source.match(/\n  desktop-go:\n([\s\S]*?)(?=\n  [a-z][a-z0-9-]*:|$)/)?.[1];
  assert.match(ordinary, /node \.\.\/scripts\/desktop-windows-go-tests\.mjs --all/);
});

test("inventory keeps same-named tests from different packages and rejects missing output", () => {
  const output = ["pkg/a", "pkg/b"].map(Package => JSON.stringify({ Action: "output", Package, Output: "TestActive\n" })).join("\n");
  assert.equal(inventoryFromJSON(output).size, 2);
  assert.throws(() => inventoryFromJSON(""), /no desktop test inventory/);
});

test("URI upgrade and preview regressions have exactly one Windows execution owner", () => {
  for (const name of ["TestWindowsUpgradeFixtureMigratesLegacyAndRestarts", "TestDesktopV1UpgradeBacksUpTopicWALAndRetriesWithoutDuplicateImport",
    "TestChatFileReferencePreservesRawFilenameCharacters", "TestResolveMarkdownImageSelectsPercentAndSpaceNamesExactly"]) {
    assert.equal(owners(name).length, 1, name);
  }
});

test("CI runs all groups separately and retains the aggregate and native probe", () => {
  const source = readFileSync(new URL("../.github/workflows/ci.yml", import.meta.url), "utf8");
  const matrixJob = source.match(/\n  desktop-windows-go-group:\n([\s\S]*?)(?=\n  [a-z][a-z0-9-]*:|$)/)?.[1];
  assert.ok(matrixJob);
  const matrix = matrixJob.match(/group: \[([^\]]+)\]/)[1].split(",").map(value => value.trim());
  assert.deepEqual(matrix, groups);
  const raceJob = source.match(/\n  desktop-go-race:\n([\s\S]*?)(?=\n  [a-z][a-z0-9-]*:|$)/)?.[1];
  assert.ok(raceJob);
  assert.deepEqual(raceJob.match(/group: \[([^\]]+)\]/)[1].split(",").map(value => value.trim()), groups);
  assert.match(matrixJob, /fail-fast: false/);
  assert.match(matrixJob, /actions\/setup-node@[0-9a-f]{40} # v7\b/);
  assert.doesNotMatch(matrixJob, /cache: pnpm/, "Go-only groups must not cache a pnpm store they never create");
  assert.match(matrixJob, /run: node \.\.\/scripts\/desktop-windows-go-tests\.mjs \$\{\{ matrix.group \}\}/);
  assert.match(matrixJob, /run: go test -run '\^TestWindowsTerminalProcessConPTYSmoke\$' \./);
  const aggregate = source.match(/\n  desktop-windows-go:\n([\s\S]*?)(?=\n  [a-z][a-z0-9-]*:|$)/)?.[1];
  assert.match(aggregate, /needs: \[changes, desktop-prepare, desktop-windows-go-group\]/);
  assert.match(aggregate, /test "\$GROUP_RESULT" = success/);
  const releaseControl = source.match(/\n  release-control:\n([\s\S]*?)(?=\n  [a-z][a-z0-9-]*:|$)/)?.[1];
  assert.ok(releaseControl, "release-control job must still exist");
  assert.match(releaseControl, /node --test[\s\S]*?scripts\/desktop-windows-go-tests\.test\.mjs/);
});
