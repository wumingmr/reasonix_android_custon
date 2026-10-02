import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { isolatedGroups, selectPackages, testArgs } from "./windows-go-tests.mjs";
import { windowsPRContractArgs, windowsPRContractGroups } from "./windows-pr-contract-tests.mjs";

const packages = ["reasonix/cmd/reasonix", "reasonix/internal/agent", "reasonix/internal/agent/testutil",
  "reasonix/internal/acp", "reasonix/internal/agentpreset", "reasonix/internal/boot", "reasonix/internal/bot", "reasonix/internal/control",
  "reasonix/internal/control/child", "reasonix/internal/extension/sidecar", "reasonix/internal/proc",
  "reasonix/internal/serve", "reasonix/internal/session", "reasonix/internal/worktree",
  "reasonix/internal/lsp", "reasonix/internal/fileops", "reasonix/internal/newpackage", "reasonix/internal/projectiondb",
  "reasonix/internal/sessioncatalog", "reasonix/internal/sqliteuri", "reasonix/internal/topicstate",
  "reasonix/internal/winaclresidue", "reasonix/tools/repolint"];

test("the full Windows groups cover every package exactly once, including new packages", () => {
  const grouped = ["full", ...isolatedGroups].flatMap(group => selectPackages(packages, group));
  assert.deepEqual(grouped.toSorted(), packages.toSorted());
  assert.equal(new Set(grouped).size, grouped.length);
  assert.ok(selectPackages(packages, "full").includes("reasonix/internal/agentpreset"));
});

test("PR smoke keeps platform coverage without duplicating isolated suites", () => {
  assert.deepEqual(selectPackages(packages, "smoke"), [
    "reasonix/cmd/reasonix", "reasonix/internal/extension/sidecar", "reasonix/internal/proc", "reasonix/internal/lsp",
    "reasonix/internal/fileops",
    "reasonix/internal/projectiondb", "reasonix/internal/sessioncatalog", "reasonix/internal/sqliteuri",
    "reasonix/internal/topicstate", "reasonix/internal/winaclresidue",
  ]);
  for (const group of isolatedGroups) {
    assert.deepEqual(testArgs(packages, group).slice(0, 4), ["test", "-p", "1", "-timeout=8m"]);
  }
  assert.equal(testArgs(packages, "bot")[4], "-json");
  assert.equal(testArgs(packages, "acp").includes("-json"), false);
  assert.deepEqual(testArgs(packages, "full").slice(0, 4), ["test", "-p", "4", "-timeout=8m"]);
  assert.throws(() => testArgs(packages, "typo"), /Unknown/);
  assert.throws(() => testArgs([], "full"), /Empty/);
});

test("CI invokes every isolated group and both residual entrypoints", () => {
  const source = readFileSync(new URL("../.github/workflows/ci.yml", import.meta.url), "utf8");
  for (const group of ["full", "smoke", "control"]) {
    assert.match(source, new RegExp(`run: node scripts/windows-go-tests\\.mjs ${group}\\n`));
  }
  const isolated = source.match(/\n  windows-isolated:\n([\s\S]*?)(?=\n  [a-z][a-z0-9-]*:|$)/)?.[1];
  assert.ok(isolated);
  const matrix = isolated.match(/group: \[([^\]]+)\]/)[1].split(",").map(value => value.trim());
  assert.deepEqual([...matrix, "control"].toSorted(), isolatedGroups.toSorted());
  assert.match(isolated, /- name: test\n(?:        #.*\n)*        timeout-minutes: 15\n/);
  assert.match(isolated, /run: node scripts\/windows-go-tests\.mjs \$\{\{ matrix.group \}\}/);
  assert.match(isolated, /fail-fast: false/);
  assert.match(isolated, /actions\/setup-node@[0-9a-f]{40} # v7\b/);
});

test("Windows PR contract selector covers shell identity, lifecycle and cancellation regressions", () => {
  const source = readFileSync(new URL("../.github/workflows/ci.yml", import.meta.url), "utf8");
  assert.match(source, /run: node scripts\/windows-pr-contract-tests\.mjs/);
  const selected = new Set(windowsPRContractGroups.flatMap(group => group.tests));
  for (const required of [
    "TestConfigJunctionAccessAndLockNames",
    "TestWorkspacePassesBashTimeout",
    "TestBashSchemaUnchangedWithSessionTemp",
    "TestBashUnsupportedOSSandboxUsesToolLayerPermissionBoundary",
    "TestOSSandboxSupportedPerPlatform",
    "TestE2EApprovalRoundTrip",
    "TestE2ECancelMidTurn",
    "TestBotGatewayStopWaitsForDispatchHandler",
    "TestBotGatewayStopWaitsForTurn",
    "TestBotGatewayStopBeforeTurnCancelPublication",
  ]) {
    assert.equal(selected.has(required), true, `${required} is missing from the Windows PR contract`);
  }
  for (const group of windowsPRContractGroups) {
    const args = windowsPRContractArgs(group);
    assert.equal(args.at(-1), group.package);
    for (const name of group.tests) assert.match(args[3], new RegExp(`\\b${name}\\b`));
  }
  const builtin = windowsPRContractGroups.find(group => group.package === "./internal/tool/builtin");
  assert.deepEqual(windowsPRContractArgs(builtin, { fullBuiltin: true }), ["test", "-timeout=5m", "./internal/tool/builtin"]);
});

test("Windows PR smoke executes the native junction and lock owners", () => {
  const native = ["pathidentity", "identitylock", "workspacelease", "desktopinstance", "sessioncatalog"]
    .map(name => `reasonix/internal/${name}`);
  assert.deepEqual(selectPackages(native, "smoke"), native);
});
