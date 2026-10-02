#!/usr/bin/env node
import { createHash } from "node:crypto";
import { existsSync, readFileSync } from "node:fs";
import { basename, dirname, join } from "node:path";
import { TraceMap, originalPositionFor } from "@jridgewell/trace-mapping";

function fail(message) {
  console.error(`symbolize: ${message}`);
  process.exit(1);
}

function args(argv) {
  const out = {};
  for (let i = 0; i < argv.length; i += 2) out[argv[i]?.replace(/^--/, "")] = argv[i + 1];
  return out;
}

const input = args(process.argv.slice(2));
if (!input.manifest || !input.bundle || !input.line || !input.column) {
  fail("usage: node packaging/symbolize.mjs --manifest <manifest.json> --bundle <bundle.js> --line <line> --column <column>");
}
if (!existsSync(input.manifest) || !existsSync(input.bundle)) fail("manifest or bundle does not exist");
const manifest = JSON.parse(readFileSync(input.manifest, "utf8"));
if (manifest.schemaVersion !== 1 || !Array.isArray(manifest.entries)) fail("unsupported source map manifest");
const bundleHash = `sha256:${createHash("sha256").update(readFileSync(input.bundle)).digest("hex")}`;
const candidates = manifest.entries.filter((entry) => basename(entry.bundle) === basename(input.bundle));
const entry = candidates.find((candidate) => candidate.bundleHash === bundleHash);
if (!entry) fail(`bundle hash ${bundleHash} is absent from the manifest`);
const mapPath = join(dirname(input.manifest), entry.map);
if (!existsSync(mapPath)) fail(`map is missing: ${entry.map}`);
const mapHash = `sha256:${createHash("sha256").update(readFileSync(mapPath)).digest("hex")}`;
if (mapHash !== entry.mapHash) fail(`map hash mismatch for ${entry.map}`);
const position = originalPositionFor(new TraceMap(JSON.parse(readFileSync(mapPath, "utf8"))), {
  line: Number(input.line),
  column: Number(input.column),
});
if (!position.source || position.line == null || position.column == null) fail("the generated position has no source mapping");
console.log(`${position.source}:${position.line}:${position.column}${position.name ? ` (${position.name})` : ""}`);
