// Shared plumbing for the per-platform Go test lanes. Group membership stays
// with each platform: Windows needs isolated groups and an explicit -p for
// Defender, macOS only needs the darwin-specific packages. Only the package
// listing, the prefix test and the runner are common.
import { spawnSync } from "node:child_process";

export const beneath = (pkg, root) => pkg === root || pkg.startsWith(`${root}/`);

export const internalRoots = (...names) => names.map(name => `reasonix/internal/${name}`);

/** `go list ./...`, or `{packages: null}` with the listing's own exit status. */
export function listPackages() {
  const listed = spawnSync("go", ["list", "./..."], { encoding: "utf8" });
  if (listed.error) throw listed.error;
  if (listed.status !== 0) {
    process.stderr.write(listed.stderr || "go list failed\n");
    return { packages: null, status: listed.status ?? 1 };
  }
  return { packages: listed.stdout.trim().split(/\r?\n/).filter(Boolean), status: 0 };
}

export function runGoTest(label, selected, args) {
  console.log(`${label}: ${selected.length} packages; go ${args.join(" ")}`);
  const result = spawnSync("go", args, { stdio: "inherit" });
  if (result.error) throw result.error;
  return result.status ?? 1;
}
