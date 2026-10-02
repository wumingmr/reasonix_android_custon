#!/usr/bin/env node
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { lstatSync, mkdirSync, readFileSync, readdirSync, readlinkSync, writeFileSync } from "node:fs";
import { basename, join, relative, resolve } from "node:path";
import { pathToFileURL } from "node:url";

function toolVersion(command, args) {
  try {
    return execFileSync(command, args, { encoding: "utf8", stdio: ["ignore", "pipe", "ignore"] }).trim().split(/\r?\n/)[0];
  } catch {
    return "unavailable";
  }
}

function category(name) {
  const path = name.toLowerCase();
  if (/(^|\/)(reasonix-desktop)(\.exe)?$/.test(path)) return "service";
  if (/electron framework|\/macos\/reasonix$|(^|\/)reasonix\.exe$/.test(path)) return "electron";
  if (/(^|\/)(reasonix|reasonix-cli)(\.exe)?$/.test(path)) return "cli";
  if (/helper|launcher|migrator|guard|uninstall/.test(path)) return "helper";
  if (/app\.asar|\/app\/|index\.html|\.cjs$/.test(path)) return "frontend";
  return "resources";
}

function sha256(path) {
  return createHash("sha256").update(readFileSync(path)).digest("hex");
}

function bundledBuildInfo(root) {
  const candidates = [];
  const walk = (directory) => {
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      const path = join(directory, entry.name);
      if (entry.isDirectory()) walk(path);
      else if (entry.isFile() && entry.name === "build.json") candidates.push(path);
    }
  };
  walk(root);
  for (const path of candidates.sort()) {
    try {
      const value = JSON.parse(readFileSync(path, "utf8"));
      if (value && typeof value === "object" && value.schemaVersion === 1) return value;
    } catch {
      // A non-Reasonix build.json remains visible in the file report.
    }
  }
  return null;
}

export function inspectTree(root) {
  const files = [];
  const symlinks = [];
  const walk = (directory) => {
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      const path = join(directory, entry.name);
      const name = relative(root, path).replaceAll("\\", "/");
      const stat = lstatSync(path);
      if (stat.isSymbolicLink()) {
        symlinks.push({ path: name, target: readlinkSync(path) });
      } else if (stat.isDirectory()) {
        walk(path);
      } else if (stat.isFile()) {
        files.push({
          path: name,
          bytes: stat.size,
          diskBytes: typeof stat.blocks === "number" ? stat.blocks * 512 : null,
          sha256: sha256(path),
          category: category(name),
        });
      }
    }
  };
  walk(root);
  const categories = {};
  for (const file of files) categories[file.category] = (categories[file.category] ?? 0) + file.bytes;
  const duplicates = Object.values(Object.groupBy(files, (file) => file.sha256))
    .filter((group) => group.length > 1)
    .map((group) => ({ sha256: group[0].sha256, bytesEach: group[0].bytes, paths: group.map((file) => file.path) }))
    .sort((a, b) => (b.bytesEach * b.paths.length) - (a.bytesEach * a.paths.length));
  return {
    logicalBytes: files.reduce((sum, file) => sum + file.bytes, 0),
    diskBytes: files.every((file) => file.diskBytes !== null) ? files.reduce((sum, file) => sum + file.diskBytes, 0) : null,
    fileCount: files.length,
    categories,
    largestFiles: [...files].sort((a, b) => b.bytes - a.bytes).slice(0, 25),
    duplicates,
    symlinks,
  };
}

function parseArgs(argv) {
  const result = {};
  for (let i = 0; i < argv.length; i += 2) {
    if (!argv[i]?.startsWith("--") || argv[i + 1] === undefined) throw new Error(`invalid argument ${argv[i] ?? ""}`);
    result[argv[i].slice(2)] = argv[i + 1];
  }
  for (const key of ["platform", "version", "bundle", "dist", "output"]) {
    if (!result[key]) throw new Error(`missing --${key}`);
  }
  return result;
}

function markdown(report) {
  const comparison = report.comparison ? [
    "## Comparison",
    "",
    `- Baseline: ${report.comparison.baseline}`,
    `- Download bytes delta: ${report.comparison.downloadBytesDelta ?? "unavailable"}`,
    `- Bundle logical bytes delta: ${report.comparison.bundleLogicalBytesDelta ?? "unavailable"}`,
    `- Bundle disk bytes delta: ${report.comparison.bundleDiskBytesDelta ?? "unavailable"}`,
    ...report.comparison.artifactDeltas.map((item) => `- ${item.name}: ${item.bytesDelta ?? "no matching baseline"} bytes`),
    "",
  ] : [];
  const lines = [
    `# Reasonix package size report: ${report.platform}`,
    "",
    `- Version: \`${report.version}\``,
    `- Source: \`${report.sourceSHA}\``,
    `- Bundle logical size: ${report.bundle.logicalBytes} bytes`,
    `- Bundle disk usage: ${report.bundle.diskBytes ?? "unavailable"} bytes`,
    `- Build duration: ${report.metrics.buildSeconds ?? "unavailable"} seconds`,
    `- Install/extract duration: ${report.metrics.installSeconds ?? "unavailable"} seconds`,
    `- Temporary disk peak: ${report.metrics.temporaryPeakBytes ?? "unavailable"} bytes`,
    "",
    "## Download artifacts",
    "",
    "| File | Bytes | SHA-256 |",
    "| --- | ---: | --- |",
    ...report.artifacts.map((item) => `| ${item.name} | ${item.bytes} | \`${item.sha256}\` |`),
    "",
    "## Bundle categories",
    "",
    "| Category | Bytes |",
    "| --- | ---: |",
    ...Object.entries(report.bundle.categories).sort().map(([name, bytes]) => `| ${name} | ${bytes} |`),
    "",
    "## Largest files",
    "",
    "| Path | Bytes | Category |",
    "| --- | ---: | --- |",
    ...report.bundle.largestFiles.map((item) => `| ${item.path} | ${item.bytes} | ${item.category} |`),
    "",
    "## Symbolic links",
    "",
    ...(report.bundle.symlinks.length ? report.bundle.symlinks.map((item) => `- \`${item.path}\` → \`${item.target}\``) : ["None."]),
    "",
    "## Duplicate content",
    "",
    ...(report.bundle.duplicates.length ? report.bundle.duplicates.map((item) => `- ${item.bytesEach} bytes × ${item.paths.length}: ${item.paths.map((path) => `\`${path}\``).join(", ")}`) : ["None."]),
    "",
    ...comparison,
  ];
  return lines.join("\n");
}

export function generateReport(options) {
  const distEntries = readdirSync(options.dist, { withFileTypes: true });
  const platformName = options.platform.replace("/", "-");
  const artifacts = distEntries
    .filter((entry) => entry.isFile() && !entry.name.endsWith(".minisig") && entry.name.startsWith(`Reasonix-${platformName}`))
    .map((entry) => {
      const path = join(options.dist, entry.name);
      return { name: entry.name, bytes: lstatSync(path).size, sha256: sha256(path) };
    })
    .sort((a, b) => a.name.localeCompare(b.name));
  const buildInfo = bundledBuildInfo(options.bundle);
  const report = {
    schemaVersion: 1,
    version: options.version,
    sourceSHA: options.sourceSHA || toolVersion("git", ["rev-parse", "HEAD"]),
    platform: options.platform,
    tools: {
      electron: options.electronVersion || buildInfo?.electron || "unavailable",
      go: toolVersion("go", ["version"]),
      node: process.version,
    },
    artifacts,
    bundle: inspectTree(options.bundle),
    metrics: {
      buildSeconds: options.buildSeconds ? Number(options.buildSeconds) : null,
      installSeconds: options.installSeconds ? Number(options.installSeconds) : null,
      temporaryPeakBytes: options.temporaryPeakBytes ? Number(options.temporaryPeakBytes) : null,
    },
    buildInfo,
  };
  if (options.baseline) {
    const baseline = JSON.parse(readFileSync(options.baseline, "utf8"));
    const baselineArtifacts = new Map((baseline.artifacts ?? []).map((artifact) => [artifact.name, artifact]));
    const currentDownloadBytes = report.artifacts.reduce((sum, artifact) => sum + artifact.bytes, 0);
    const baselineDownloadBytes = (baseline.artifacts ?? []).reduce((sum, artifact) => sum + Number(artifact.bytes ?? 0), 0);
    report.comparison = {
      baseline: options.baselineLabel || `${baseline.version ?? "unknown"} ${baseline.platform ?? ""}`.trim(),
      artifactDeltas: report.artifacts.map((artifact) => ({
        name: artifact.name,
        baselineBytes: baselineArtifacts.get(artifact.name)?.bytes ?? null,
        bytesDelta: baselineArtifacts.has(artifact.name) ? artifact.bytes - Number(baselineArtifacts.get(artifact.name).bytes) : null,
      })),
      downloadBytesDelta: report.artifacts.length === (baseline.artifacts ?? []).length && report.artifacts.every((artifact) => baselineArtifacts.has(artifact.name))
        ? currentDownloadBytes - baselineDownloadBytes
        : null,
      bundleLogicalBytesDelta: baseline.bundle?.logicalBytes !== undefined
        ? report.bundle.logicalBytes - Number(baseline.bundle.logicalBytes)
        : null,
      bundleDiskBytesDelta: report.bundle.diskBytes !== null && baseline.bundle?.diskBytes !== null
        ? report.bundle.diskBytes - Number(baseline.bundle?.diskBytes ?? 0)
        : null,
    };
  }
  mkdirSync(options.output, { recursive: true });
  writeFileSync(join(options.output, "size-report.json"), JSON.stringify(report, null, 2) + "\n");
  writeFileSync(join(options.output, "size-report.md"), markdown(report));
  return report;
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  const args = parseArgs(process.argv.slice(2));
  generateReport({
    ...args,
    bundle: resolve(args.bundle),
    dist: resolve(args.dist),
    output: resolve(args.output),
    sourceSHA: process.env.REASONIX_COMMIT,
    buildSeconds: process.env.REASONIX_BUILD_SECONDS,
    installSeconds: process.env.REASONIX_INSTALL_SECONDS,
    temporaryPeakBytes: process.env.REASONIX_TEMPORARY_PEAK_BYTES,
    baseline: args.baseline || process.env.REASONIX_SIZE_BASELINE_REPORT,
    baselineLabel: args["baseline-label"] || process.env.REASONIX_SIZE_BASELINE_LABEL,
  });
}
