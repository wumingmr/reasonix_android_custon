import assert from "node:assert/strict";
import test from "node:test";
import { platformPackage, renderHomebrewCask, targets } from "./build-release-cli-candidate.mjs";

test("platform package embeds the immutable full candidate identity", () => {
  const pkg = platformPackage(targets[0], "1.2.3", "a".repeat(40));
  assert.equal(pkg.name, "@reasonix/cli-darwin-arm64");
  assert.equal(pkg.reasonixCandidateSha, "a".repeat(40));
  assert.equal(pkg.gitHead, "a".repeat(40));
});

test("homebrew cask is checksum-bound to prepared CLI archives", () => {
  const sums = Object.fromEntries(targets.filter(target => target.goos !== "windows").map(target => [`reasonix-${target.key}.tar.gz`, target.key.includes("arm64") ? "a".repeat(64) : "b".repeat(64)]));
  const cask = renderHomebrewCask("1.2.3", sums);
  assert.match(cask, /version "1\.2\.3"/);
  assert.match(cask, /sha256 "a{64}"/);
  assert.match(cask, /releases\/download\/v#\{version\}/);
});
