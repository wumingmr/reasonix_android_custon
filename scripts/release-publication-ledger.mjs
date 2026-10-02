import { readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";

const VERSION_RE = /^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/;
const SHA_RE = /^[0-9a-f]{40}$/;

export const FROZEN_HOMEBREW = "frozen";

export const npmPackageNames = [
  "reasonix",
  "@reasonix/cli-darwin-arm64",
  "@reasonix/cli-darwin-x64",
  "@reasonix/cli-linux-arm64",
  "@reasonix/cli-linux-x64",
  "@reasonix/cli-win32-arm64",
  "@reasonix/cli-win32-x64",
];

function compareStable(a, b) {
  if (!VERSION_RE.test(a) || !VERSION_RE.test(b)) throw new Error("invalid stable version in publication observation");
  const aa = a.split(".").map(Number);
  const bb = b.split(".").map(Number);
  for (let index = 0; index < aa.length; index += 1) {
    if (aa[index] !== bb[index]) return aa[index] > bb[index] ? 1 : -1;
  }
  return 0;
}

export function ownsStablePointer(version, operation, manifest) {
  if (!["publish", "recover"].includes(operation)) throw new Error("invalid publication operation");
  const current = manifest?.version;
  if (typeof current !== "string" || !current.startsWith("v")) throw new Error("missing or invalid Stable manifest version");
  const comparison = compareStable(current.slice(1), version);
  if (comparison === 0) return true;
  if (comparison > 0 && operation === "recover") return false;
  throw new Error(`Stable manifest serves ${current}, want v${version}`);
}

function requireIdentity(version, sourceSHA, operation) {
  if (!VERSION_RE.test(version)) throw new Error("invalid publication ledger version");
  if (!SHA_RE.test(sourceSHA)) throw new Error("invalid publication ledger source SHA");
  if (!["publish", "recover"].includes(operation)) throw new Error("invalid publication operation");
}

function releaseAssets(release, surface) {
  if (release?.isDraft !== false || release?.isPrerelease !== false || !Array.isArray(release.assets)) {
    throw new Error(`${surface} release is not a public final release`);
  }
  return release.assets.map(asset => ({
    name: asset.name,
    size: asset.size,
    digest: asset.digest || null,
    state: "identity-verified",
  })).sort((a, b) => a.name.localeCompare(b.name));
}

export function createCoreLedger({ version, sourceSHA, operation, cliRelease, desktopRelease, npmPackages, observedAt = new Date().toISOString() }) {
  requireIdentity(version, sourceSHA, operation);
  if (!Array.isArray(npmPackages) || npmPackages.length !== npmPackageNames.length) {
    throw new Error("publication ledger requires all npm packages");
  }
  const packages = npmPackages.map(item => {
    if (!npmPackageNames.includes(item.name) || item.version !== version || !item.integrity) {
      throw new Error(`invalid npm publication observation: ${item.name ?? "<unknown>"}`);
    }
    if ((item.reasonixCandidateSha && item.reasonixCandidateSha !== sourceSHA)
        || (item.gitHead && item.gitHead !== sourceSHA)
        || (!item.reasonixCandidateSha && !item.gitHead)) {
      throw new Error(`npm package does not match the candidate: ${item.name}`);
    }
    const frozenTag = item.distTag !== undefined;
    if (frozenTag && item.distTag !== "legacy-v1") throw new Error(`unexpected npm dist-tag for ${item.name}: ${item.distTag}`);
    const observed = frozenTag ? item.distTagVersion : item.latest;
    const pointerComparison = compareStable(observed, version);
    if (pointerComparison < 0 || (operation === "publish" && pointerComparison !== 0)) {
      throw new Error(`npm ${frozenTag ? item.distTag : "latest"} is inconsistent for ${item.name}: ${observed}`);
    }
    return {
      name: item.name,
      version: item.version,
      integrity: item.integrity,
      ...(frozenTag ? { distTag: item.distTag, distTagVersion: observed } : { latest: observed }),
      state: "identity-verified",
      pointerState: pointerComparison === 0 ? "public-entry-updated" : "newer-entry-preserved",
    };
  }).sort((a, b) => a.name.localeCompare(b.name));
  if (new Set(packages.map(item => item.name)).size !== npmPackageNames.length) {
    throw new Error("publication ledger contains duplicate npm packages");
  }
  for (const name of npmPackageNames) {
    if (!packages.some(item => item.name === name)) throw new Error(`publication ledger is missing npm package: ${name}`);
  }
  return {
    schema: 1,
    version,
    sourceSHA,
    operation,
    observedAt,
    surfaces: {
      tags: {
        state: "identity-verified",
        items: [`v${version}`, `npm-v${version}`, `desktop-v${version}`].map(name => ({ name, sha: sourceSHA })),
      },
      cli: { state: "identity-verified", assets: releaseAssets(cliRelease, "CLI") },
      npm: { state: "identity-verified", packages },
      desktop: { state: "identity-verified", assets: releaseAssets(desktopRelease, "Desktop") },
    },
  };
}

export function createPointerLedger({ version, sourceSHA, operation, manifest, homebrewVersion, observedAt = new Date().toISOString() }) {
  requireIdentity(version, sourceSHA, operation);
  if (manifest?.version !== `v${version}`) throw new Error("Stable manifest does not match the publication ledger");
  const homebrewFrozen = homebrewVersion === FROZEN_HOMEBREW;
  if (!homebrewFrozen && homebrewVersion !== version) throw new Error("Homebrew cask does not match the publication ledger");
  return {
    schema: 1,
    version,
    sourceSHA,
    operation,
    observedAt,
    surfaces: {
      stableManifest: { state: "public-entry-updated", version: manifest.version },
      ...(homebrewFrozen ? {} : { homebrew: { state: "public-entry-updated", version } }),
    },
  };
}

export function mergeLedgers(core, pointers, observedAt = new Date().toISOString()) {
  if (core.schema !== 1 || pointers.schema !== 1 || core.version !== pointers.version
      || core.sourceSHA !== pointers.sourceSHA || core.operation !== pointers.operation) {
    throw new Error("publication ledger fragments do not describe one release");
  }
  return { ...core, observedAt, surfaces: { ...core.surfaces, ...pointers.surfaces } };
}

function read(file) {
  return JSON.parse(readFileSync(file, "utf8"));
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  const [command, ...args] = process.argv.slice(2);
  if (command === "pointer-owner" && args.length === 3) {
    const [version, operation, manifestPath] = args;
    console.log(ownsStablePointer(version, operation, read(manifestPath)));
  } else if (command === "core" && args.length === 7) {
    const [version, sourceSHA, operation, cliPath, desktopPath, npmPath, output] = args;
    writeFileSync(output, `${JSON.stringify(createCoreLedger({ version, sourceSHA, operation, cliRelease: read(cliPath), desktopRelease: read(desktopPath), npmPackages: read(npmPath) }), null, 2)}\n`);
  } else if (command === "pointers" && args.length === 6) {
    const [version, sourceSHA, operation, manifestPath, homebrewVersion, output] = args;
    writeFileSync(output, `${JSON.stringify(createPointerLedger({ version, sourceSHA, operation, manifest: read(manifestPath), homebrewVersion }), null, 2)}\n`);
  } else if (command === "merge" && args.length === 3) {
    const [corePath, pointersPath, output] = args;
    writeFileSync(output, `${JSON.stringify(mergeLedgers(read(corePath), read(pointersPath)), null, 2)}\n`);
  } else {
    throw new Error("usage: release-publication-ledger.mjs pointer-owner VERSION OPERATION MANIFEST | core VERSION SHA OPERATION CLI DESKTOP NPM OUTPUT | pointers VERSION SHA OPERATION MANIFEST HOMEBREW_VERSION OUTPUT | merge CORE POINTERS OUTPUT");
  }
}
