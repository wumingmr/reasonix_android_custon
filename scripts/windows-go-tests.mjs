import { resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { beneath, internalRoots, listPackages, runGoTest } from "./go-test-groups.mjs";

// Start the long filesystem suites immediately on independent runners instead
// of leaving them behind hundreds of short packages in the residual queue.
export const isolatedGroups = ["acp", "agent", "boot", "bot", "control", "serve", "session", "worktree"];
const smokeRoots = internalRoots(
  "appidentity", "checkpoint", "cli", "desktoplauncher", "extension/sidecar",
  "desktopinstance", "filelock", "fileops", "fileutil", "hook", "identitylock", "instruction", "mcplaunch", "notify",
  // persistentshell exercises native Git Bash pipes and PowerShell named pipes;
  // Windows is the only lane that can prove the actual Windows runtimes.
  "persistentshell", "proc",
  "lsp", "pathidentity", "projectiondb", "remote", "repair", "sandbox", "sessioncatalog", "sqliteuri", "sysproxy",
  "topicstate", "winaclresidue", "workspacelease",
).concat("reasonix/cmd");

export function selectPackages(packages, group) {
  if (!["full", "smoke", ...isolatedGroups].includes(group)) {
    throw new Error(`Unknown Windows test group: ${group}`);
  }
  const isolated = pkg => isolatedGroups.some(name => beneath(pkg, `reasonix/internal/${name}`));
  return packages.filter(pkg => {
    if (isolatedGroups.includes(group)) return beneath(pkg, `reasonix/internal/${group}`);
    return !isolated(pkg) && (group === "full" || smokeRoots.some(root => beneath(pkg, root)));
  });
}

export function testArgs(packages, group) {
  const selected = selectPackages(packages, group);
  if (selected.length === 0) throw new Error(`Empty Windows test group: ${group}`);
  const args = ["test", "-p", isolatedGroups.includes(group) ? "1" : "4", "-timeout=8m"];
  // Bot has produced process-level Windows exits without a Go stack or test
  // name. JSON preserves the last start/output event and the subprocess exit
  // status while keeping the whole package in one process (no retry or split).
  if (group === "bot") args.push("-json");
  return [...args, ...selected];
}

function main(group) {
  const { packages, status } = listPackages();
  if (!packages) return status;
  return runGoTest(`Windows ${group}`, selectPackages(packages, group), testArgs(packages, group));
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.exitCode = main(process.argv[2]);
}
