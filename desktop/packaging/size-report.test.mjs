import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, readFileSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { generateReport, inspectTree } from "./size-report.mjs";

test("size report classifies files, duplicates and links without following links", () => {
  const root = mkdtempSync(join(tmpdir(), "reasonix-size-report-"));
  mkdirSync(join(root, "bundle", "service"), { recursive: true });
  writeFileSync(join(root, "bundle", "service", "reasonix-desktop"), "service");
  writeFileSync(join(root, "bundle", "reasonix"), "same");
  writeFileSync(join(root, "bundle", "copy"), "same");
  symlinkSync("service/reasonix-desktop", join(root, "bundle", "service-link"));
  mkdirSync(join(root, "dist"));
  writeFileSync(join(root, "dist", "Reasonix-linux-amd64.tar.gz"), "archive");

  const tree = inspectTree(join(root, "bundle"));
  assert.equal(tree.fileCount, 3);
  assert.equal(tree.symlinks[0].target, "service/reasonix-desktop");
  assert.equal(tree.duplicates.length, 1);

  const report = generateReport({
    platform: "linux/amd64", version: "v1.2.3", sourceSHA: "a".repeat(40),
    bundle: join(root, "bundle"), dist: join(root, "dist"), output: join(root, "out"),
  });
  assert.equal(report.artifacts[0].name, "Reasonix-linux-amd64.tar.gz");
  assert.match(readFileSync(join(root, "out", "size-report.md"), "utf8"), /Bundle categories/);

  const baseline = join(root, "baseline.json");
  writeFileSync(baseline, JSON.stringify(report));
  const compared = generateReport({
    platform: "linux/amd64", version: "v1.2.4", sourceSHA: "b".repeat(40),
    bundle: join(root, "bundle"), dist: join(root, "dist"), output: join(root, "compared"), baseline,
  });
  assert.equal(compared.comparison.downloadBytesDelta, 0);
  assert.equal(compared.comparison.bundleLogicalBytesDelta, 0);
});
