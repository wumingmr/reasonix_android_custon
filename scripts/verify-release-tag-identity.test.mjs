import assert from "node:assert/strict";
import test from "node:test";
import { mkdtempSync, writeFileSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { verifyIdentity } from "./verify-release-tag-identity.mjs";

const rule = { id: 1, target: "tag", enforcement: "active", current_user_can_bypass: "always", conditions: { ref_name: { include: ["refs/tags/v*", "refs/tags/npm-v*", "refs/tags/desktop-v*"], exclude: [] } }, rules: [{ type: "creation" }, { type: "update" }, { type: "deletion" }] };
const fixture = () => ({ actor: { login: "publisher", id: 42, type: "User" }, repo: { full_name: "esengine/DeepSeek-Reasonix", permissions: { push: true } }, rulesets: [structuredClone(rule)], expectedActor: "publisher", version: "1.2.3" });

test("authorized identity, unrelated rules, and immutable-only restrictions", () => {
  assert.equal(verifyIdentity(fixture()).actorID, "42");
  const input = fixture();
  input.rulesets.push({ ...rule, id: 2, current_user_can_bypass: "never", conditions: { ref_name: { include: ["refs/tags/unrelated-*"], exclude: [] } } });
  assert.deepEqual(verifyIdentity(input).rulesets, [1]);
  input.rulesets[0].rules = [{ type: "update" }, { type: "deletion" }];
  input.rulesets[0].current_user_can_bypass = "never";
  verifyIdentity(input);
});

test("the GH013 mechanism fails before approval, including inherited and unknown rules", () => {
  for (const bypass of ["never", "pull_request", undefined]) {
    const input = fixture();
    input.rulesets[0].current_user_can_bypass = bypass;
    assert.throws(() => verifyIdentity(input), /cannot bypass/);
  }
  for (const pattern of ["~ALL", "refs/tags/*", "refs/tags/v[0-9]*", "refs/**", "~UNKNOWN"]) {
    const input = fixture();
    input.rulesets.push({ ...rule, id: 2, source_type: "Organization", current_user_can_bypass: "never", conditions: { ref_name: { include: [pattern], exclude: [] } } });
    assert.throws(() => verifyIdentity(input), /ruleset 2/);
  }
});

test("missing credentials, mismatched actor, token rotation, and repository permissions fail closed", () => {
  for (const change of [
    { expectedActor: "" }, { expectedActor: "someone-else" }, { expectedID: "99" },
    { actor: { login: "publisher", id: 42, type: "Bot" } },
    { repo: { full_name: "esengine/DeepSeek-Reasonix", permissions: { push: false } } },
  ]) assert.throws(() => verifyIdentity({ ...fixture(), ...change }));
});

test("CLI uses credential-scoped paginated observations and exports the bound actor ID", () => {
  const root = mkdtempSync(path.join(tmpdir(), "tag-identity-"));
  try {
    const gh = path.join(root, "gh");
    const input = fixture();
    writeFileSync(gh, `#!/usr/bin/env node
const args = process.argv.slice(2);
const responses = ${JSON.stringify({ user: input.actor, "repos/esengine/DeepSeek-Reasonix": input.repo, "repos/esengine/DeepSeek-Reasonix/rulesets?includes_parents=true&per_page=100": [[rule]], "repos/esengine/DeepSeek-Reasonix/rulesets/1?includes_parents=true": rule })};
if (process.env.GH_TOKEN !== 'test-credential') process.exit(2);
if (args.at(-1).includes('per_page') && (!args.includes('--paginate') || !args.includes('--slurp'))) process.exit(3);
if (!(args.at(-1) in responses)) process.exit(4);
console.log(JSON.stringify(responses[args.at(-1)]));
`, { mode: 0o755 });
    const env = { ...process.env, PATH: `${root}:${process.env.PATH}`, GH_TOKEN: "test-credential", RELEASE_TAG_ACTOR: "publisher", RELEASE_TAG_ACTOR_ID: "", GITHUB_ACTIONS: "true", GITHUB_REF: "refs/heads/main-v2", GITHUB_REF_PROTECTED: "true", GITHUB_REPOSITORY: "esengine/DeepSeek-Reasonix", GITHUB_OUTPUT: path.join(root, "output") };
    const run = extra => spawnSync(process.execPath, ["scripts/verify-release-tag-identity.mjs", "1.2.3"], { env: { ...env, ...extra }, encoding: "utf8" });
    const result = run({});
    assert.equal(result.status, 0, result.stderr);
    assert.equal(readFileSync(env.GITHUB_OUTPUT, "utf8"), "actor_id=42\n");
    assert.notEqual(run({ GH_TOKEN: "" }).status, 0);
    assert.notEqual(run({ GITHUB_REF: "refs/heads/untrusted" }).status, 0);
    assert.notEqual(run({ GITHUB_REF_PROTECTED: "false" }).status, 0);
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test("workflow binds preflight and activation to the same credential without checkout fallback", () => {
  const workflow = readFileSync(".github/workflows/release-promote.yml", "utf8");
  const preflight = workflow.split("\n  authorize:")[0];
  const activate = workflow.split("\n  activate:")[1].split("\n  cli:")[0];
  assert.match(preflight, /id: identity/);
  assert.match(preflight, /secrets.RELEASE_TAG_TOKEN/);
  assert.match(activate, /secrets.RELEASE_TAG_TOKEN/);
  assert.match(activate, /needs.preflight.outputs.tag_actor_id/);
  assert.match(activate, /persist-credentials: false/);
  assert.ok(activate.indexOf("verify-release-tag-identity.mjs") < activate.indexOf("release-candidate-tags.sh activate"));
  assert.match(activate, /gh auth setup-git --hostname github.com/);
});
