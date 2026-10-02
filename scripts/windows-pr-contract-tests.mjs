import { resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { runGoTest } from "./go-test-groups.mjs";

export const windowsPRContractGroups = [
  { package: "./internal/config", tests: ["TestConfigJunctionAccessAndLockNames",
    "TestCredentialAccessRepairsLegacyDenyThroughLinkedHome", "TestCredentialSaveResetsDenyWithoutSandboxRecord"] },
  {
    package: "./internal/tool/builtin",
    tests: [
      "TestBashPowerShellExecuteDetailedContract",
      "TestBashPowerShell51PreflightRejectsAndAndDetailed",
      "TestBashPowerShellOutputIsUTF8",
      "TestBashPowerShellSurfacesNonZeroExit",
      "TestBashPowerShellRejectsChaining",
      "TestBashSharesSessionTempAcrossCalls",
      "TestWorkspacePassesBashTimeout",
      "TestBashSchemaUnchangedWithSessionTemp",
      "TestBashUnsupportedOSSandboxUsesToolLayerPermissionBoundary",
      "TestBashPersistentWindowsUnicodeAndState",
    ],
  },
  { package: "./internal/sandbox", tests: ["TestOSSandboxSupportedPerPlatform"] },
  {
    package: "./internal/acp",
    tests: ["TestE2EApprovalRoundTrip", "TestE2ECancelMidTurn"],
  },
  {
    package: "./internal/bot",
    tests: [
      "TestBotGatewayStopWaitsForDispatchHandler",
      "TestBotGatewayStopWaitsForTurn",
      "TestBotGatewayStopBeforeTurnCancelPublication",
    ],
  },
];

export function windowsPRContractArgs(group, { fullBuiltin = false } = {}) {
  if (fullBuiltin && group.package === "./internal/tool/builtin") {
    return ["test", "-timeout=5m", group.package];
  }
  return ["test", "-timeout=3m", "-run", `^(?:${group.tests.join("|")})$`, group.package];
}

function main() {
  for (const group of windowsPRContractGroups) {
    const args = windowsPRContractArgs(group, { fullBuiltin: process.env.WINDOWS_BUILTIN_FULL === "true" });
    const status = runGoTest(`Windows PR contract ${group.package}`, [group.package], args);
    if (status !== 0) return status;
  }
  return 0;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.exitCode = main();
}
