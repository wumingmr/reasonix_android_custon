#!/usr/bin/env node
// Lists a desktop artifact (bundle directory, .zip, .tar.gz or .deb) and fails
// unless every member the install layout relies on is present. Linux archives
// also fail when a directory or file would be unreadable to other users, or
// when one of their Go binaries is dynamically linked.
//
// usage: node desktop/packaging/verify.mjs <artifact> [--kind <kind>] [--list]
import { spawnSync } from "node:child_process";
import { readdirSync, readFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { ARTIFACT_KINDS, WINDOWS_PORTABLE_LAYOUTS, checkEntryModes, checkMembers, checkStaticGoMembers, normalizeEntry, inferArtifactKind, isDirectory, listZipEntries, readZipMember, parseVerboseListing, validateMacServiceLink, walkFiles } from "./lib.mjs";

const args = process.argv.slice(2);
const artifactArg = args.find((arg) => !arg.startsWith("--"));
const kindIndex = args.indexOf("--kind");
const kindArg = kindIndex >= 0 ? args[kindIndex + 1] : undefined;
const layoutIndex = args.indexOf("--portable-layout");
const portableLayout = layoutIndex >= 0 ? args[layoutIndex + 1] : "canonical";
if (!artifactArg || (kindArg && !ARTIFACT_KINDS.includes(kindArg)) || !WINDOWS_PORTABLE_LAYOUTS.includes(portableLayout)) {
  console.error(`usage: verify.mjs <artifact> [--kind <${ARTIFACT_KINDS.join("|")}>] [--portable-layout canonical|legacy-dual] [--list]`);
  process.exit(2);
}
const artifact = resolve(artifactArg);

function tool(command, toolArgs) {
  const result = spawnSync(command, toolArgs, { encoding: "utf8", maxBuffer: 64 * 1024 * 1024 });
  if (result.status !== 0) throw new Error(`${command} ${toolArgs.join(" ")} failed: ${result.stderr || result.error?.message || result.status}`);
  return result.stdout.split(/\r?\n/).filter((line) => line !== "");
}

function listingOf(path) {
  if (isDirectory(path)) return { entries: walkFiles(path), rows: [] };
  if (path.endsWith(".zip")) return { entries: listZipEntries(path), rows: [] };
  let rows;
  if (path.endsWith(".tar.gz")) rows = parseVerboseListing(tool("tar", ["-tvzf", path]));
  else if (path.endsWith(".deb")) rows = parseVerboseListing(tool("dpkg-deb", ["-c", path]));
  else throw new Error(`unsupported artifact ${path}`);
  return { entries: rows.map((row) => row.name), rows };
}

const directory = isDirectory(artifact);
const kind = kindArg ?? inferArtifactKind(artifact, directory, directory ? readdirSync(artifact) : []);
const { entries, rows } = listingOf(artifact);
if (args.includes("--list")) for (const entry of entries) console.log(entry);
const { missing, forbidden } = checkMembers(entries, kind, portableLayout);
const layoutErrors = kind === "darwin-app-dir" ? validateMacServiceLink(artifact) : [];
if (kind === "windows-portable-zip" && missing.length === 0 && forbidden.length === 0) {
  const readMember = name => directory ? readFileSync(join(artifact, name)) : readZipMember(artifact, name);
  const gui = readMember("Reasonix.exe");
  if (gui.equals(readMember("reasonix-cli.exe"))) layoutErrors.push("GUI entry contains CLI bytes");
  if (portableLayout === "legacy-dual" && !gui.equals(readMember("reasonix-launcher.exe"))) {
    layoutErrors.push("legacy GUI entry differs from Reasonix.exe");
  }
}
const modeErrors = checkEntryModes(rows, kind);
if ((kind === "linux-tar" || kind === "linux-deb") && missing.length === 0) {
  const archived = new Map(rows.map((row) => [normalizeEntry(row.name), row.name]));
  const extract = kind === "linux-tar" ? 'tar -xzOf "$1" "$2"' : 'dpkg-deb --fsys-tarfile "$1" | tar -xOf - "$2"';
  const readMember = (name) => {
    const result = spawnSync("sh", ["-c", extract, "sh", artifact, archived.get(name)], { maxBuffer: 512 * 1024 * 1024 });
    if (result.status !== 0) throw new Error(`extracting ${name} failed: ${result.stderr?.toString() || result.error?.message || result.status}`);
    return result.stdout;
  };
  layoutErrors.push(...checkStaticGoMembers(kind, readMember));
}
for (const name of missing) console.error(`verify: ${kind} is missing ${name}`);
for (const name of forbidden) console.error(`verify: ${kind} must not contain ${name}`);
for (const error of layoutErrors) console.error(`verify: ${kind} ${error}`);
for (const error of modeErrors) console.error(`verify: ${kind} ${error}`);
if (missing.length > 0 || forbidden.length > 0 || layoutErrors.length > 0 || modeErrors.length > 0) process.exit(1);
console.log(`verify: ${kind} ok (${entries.length} entries in ${artifact})`);
