import assert from "node:assert/strict";
import test from "node:test";
import { decideCaskUpdate } from "./publish-homebrew-cask.mjs";

const cask = version => `cask "reasonix" do\n  version "${version}"\nend\n`;

test("reuses an identical cask", () => assert.equal(decideCaskUpdate(cask("1.2.3"), cask("1.2.3")), "reuse"));
test("publishes a newer cask", () => assert.equal(decideCaskUpdate(cask("1.2.3"), cask("1.2.4")), "publish"));
test("rejects downgrade and same-version conflict", () => {
  assert.throws(() => decideCaskUpdate(cask("2.0.0"), cask("1.9.9")), /backwards/);
  assert.throws(() => decideCaskUpdate(`${cask("1.2.3")}# old`, cask("1.2.3")), /conflicting/);
});
