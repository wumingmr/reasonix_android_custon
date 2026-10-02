import assert from "node:assert/strict";
import test from "node:test";
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync, realpathSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";

const workflow = name => readFileSync(`.github/workflows/${name}`, "utf8");
const SWITCH = "CLI_PUBLISH_FROZEN";

// Evaluates a workflow expression with the JavaScript operators the release
// workflows use, so a default (variable unset) can be compared with a frozen run.
function evaluate(expression, { frozen, channel = "stable", token = "TAP-TOKEN" }) {
  const vars = { [SWITCH]: String(frozen).toLowerCase() };
  const needs = { resolve: { outputs: { channel } } };
  const secrets = { HOMEBREW_TAP_TOKEN: token };
  const js = expression.replace(/==/g, "===").replace(/!=/g, "!==");
  return new Function("vars", "needs", "secrets", "inputs", `return (${js});`)(vars, needs, secrets, { candidate_artifact_name: "candidate", channel: "stable" });
}

function expressionAfter(file, anchor) {
  const text = workflow(file);
  const index = text.indexOf(anchor);
  assert.ok(index >= 0, `${file} lost: ${anchor}`);
  return text.slice(index + anchor.length).match(/\$\{\{ (.*?) \}\}/)[1];
}

test("every use of the switch is either a guard or a default-preserving selection", () => {
  for (const file of ["release.yml", "release-npm.yml", "release-promote.yml", "release-stable.yml", "release-verify.yml"]) {
    for (const line of workflow(file).split("\n").filter(item => item.includes(`vars.${SWITCH}`))) {
      const allowed = [
        /^\s+if: \$\{\{ .*vars\.CLI_PUBLISH_FROZEN != 'true'.* \}\}$/,
        /^\s+NPM_STABLE_DIST_TAG: \$\{\{ vars\.CLI_PUBLISH_FROZEN == 'true' && 'legacy-v1' \|\| 'latest' \}\}$/,
        /^\s+args: release --clean\$\{\{ vars\.CLI_PUBLISH_FROZEN == 'true' && ' --skip=homebrew' \|\| '' \}\}$/,
        /^\s+HOMEBREW_TAP_TOKEN: \$\{\{ .*vars\.CLI_PUBLISH_FROZEN != 'true' && secrets\.HOMEBREW_TAP_TOKEN \|\| '' \}\}$/,
        /^\s+CLI_PUBLISH_FROZEN: \$\{\{ vars\.CLI_PUBLISH_FROZEN == 'true' \}\}$/,
      ];
      assert.ok(allowed.some(pattern => pattern.test(line)), `${file}: unexpected use of the switch: ${line.trim()}`);
    }
  }
});

const TOKEN = "HOMEBREW_TAP_TOKEN: ";
const CASK_IF = "- name: Publish prepared Homebrew cask\n        if: ";
const ALIGN_IF = "- name: Align legacy aliases with the official release\n        if: ";
const ARGS = "args: release --clean";
const DIST_TAG = "NPM_STABLE_DIST_TAG: ";

test("with the variable unset the CLI publication expressions evaluate as they always did", () => {
  const unset = { frozen: "" };
  assert.equal(evaluate(expressionAfter("release.yml", ARGS), unset), "");
  assert.equal(evaluate(expressionAfter("release.yml", TOKEN), unset), "TAP-TOKEN");
  assert.equal(evaluate(expressionAfter("release.yml", TOKEN), { ...unset, channel: "preview" }), "");
  assert.equal(evaluate(expressionAfter("release.yml", CASK_IF), unset), true);
  assert.equal(evaluate(expressionAfter("release-npm.yml", DIST_TAG), unset), "latest");
  assert.equal(evaluate(expressionAfter("release-npm.yml", ALIGN_IF), unset), true);
});

test("with the variable set to true the CLI channels are skipped and npm moves to legacy-v1", () => {
  const frozen = { frozen: "true" };
  assert.equal(evaluate(expressionAfter("release.yml", ARGS), frozen), " --skip=homebrew");
  assert.equal(evaluate(expressionAfter("release.yml", TOKEN), frozen), "");
  assert.equal(evaluate(expressionAfter("release.yml", CASK_IF), frozen), false);
  assert.equal(evaluate(expressionAfter("release-npm.yml", DIST_TAG), frozen), "legacy-v1");
  assert.equal(evaluate(expressionAfter("release-npm.yml", ALIGN_IF), frozen), false);
  for (const value of ["false", "1", "TRUE-ish", ""]) {
    assert.equal(evaluate(expressionAfter("release-npm.yml", DIST_TAG), { frozen: value }), "latest", value);
  }
});

test("the frozen npm publisher can never write latest, canary or next", () => {
  const npm = workflow("release-npm.yml");
  const align = npm.split("- name: Align legacy aliases with the official release")[1].split("\n      - ")[0];
  assert.match(align, /vars\.CLI_PUBLISH_FROZEN != 'true'/);
  assert.equal((npm.match(/NPM_STABLE_DIST_TAG: /g) || []).length, 2);
  const publishSteps = npm.split("- name: Publish or recover ").slice(1);
  assert.equal(publishSteps.length, 2);
  for (const step of publishSteps) assert.match(step.split("\n      - ")[0], /NPM_STABLE_DIST_TAG/);
});

test("R2 pointers are guarded after the immutable record and before any pointer read or write", () => {
  const step = workflow("release.yml").split("- name: Publish CLI release metadata to R2")[1].split("- name: Attach desktop manifest")[0];
  const immutable = step.indexOf('immutable_key="cli/releases/${TAG}/latest.json"');
  const guard = step.indexOf('if [ "${CLI_PUBLISH_FROZEN:-}" = "true" ]; then');
  const pointerRead = step.indexOf('"s3://${R2_BUCKET}/cli/${channel}/latest.json" /tmp/cli-release.pointer.json');
  const pointerWrite = step.indexOf('aws s3 cp /tmp/cli-release.json "s3://${R2_BUCKET}/cli/${channel}/latest.json"');
  assert.ok(immutable > 0 && immutable < guard && guard < pointerRead && guard < pointerWrite);
  assert.match(step, /CLI_PUBLISH_FROZEN: \$\{\{ vars\.CLI_PUBLISH_FROZEN == 'true' \}\}/);
});

test("Desktop publication and the release orchestration do not depend on the switch", () => {
  assert.equal(workflow("release-desktop.yml").includes(SWITCH), false);
  for (const file of ["release-promote.yml", "release-stable.yml"]) {
    const text = workflow(file);
    for (const job of ["cli", "npm", "desktop"]) {
      const block = text.split(new RegExp(`^  ${job}:\\n`, "m"))[1].split(/^  [a-z-]+:\n/m)[0];
      assert.equal(block.includes(SWITCH), false, `${file} job ${job} must not be skipped by the switch`);
    }
  }
  assert.match(workflow("release-promote.yml"), /CLI_PUBLISH_FROZEN: \$\{\{ vars\.CLI_PUBLISH_FROZEN == 'true' \}\}\n/);
  assert.match(workflow("release-promote.yml"), /needs: \[preflight, cli, npm, desktop\]/);
});

const REQUIRED_CLI = ["SHA256SUMS", "reasonix-darwin-amd64.tar.gz", "reasonix-darwin-arm64.tar.gz", "reasonix-linux-amd64.tar.gz", "reasonix-linux-arm64.tar.gz", "reasonix-windows-amd64.zip", "reasonix-windows-arm64.zip"];
const DESKTOP = ["Reasonix-darwin-arm64.dmg", "Reasonix-darwin-amd64.dmg", "Reasonix-darwin-universal.dmg", "Reasonix-darwin-arm64.zip", "Reasonix-darwin-amd64.zip", "Reasonix-linux-amd64.deb", "Reasonix-linux-amd64.tar.gz", "Reasonix-windows-amd64-installer.exe", "Reasonix-windows-amd64.zip", "Reasonix-windows-arm64-installer.exe"];
const sha = "a".repeat(40);

function verifier(t, { latest = "1.2.3", legacy = "1.2.3" } = {}) {
  const root = realpathSync(mkdtempSync(path.join(tmpdir(), "cli-freeze-")));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const bin = path.join(root, "bin");
  mkdirSync(bin);
  const asset = name => ({ name, size: 1 });
  const cli = { isDraft: false, isPrerelease: false, assets: REQUIRED_CLI.map(asset) };
  const desktop = { isDraft: false, isPrerelease: false, assets: [asset("latest.json"), ...DESKTOP.flatMap(name => [asset(name), asset(`${name}.minisig`)])] };
  writeFileSync(path.join(root, "state.json"), JSON.stringify({ cli, desktop, latest, legacy }));
  writeFileSync(path.join(bin, "git"), `#!/usr/bin/env node\nconsole.log('${sha}\\trefs/tags/x');`, { mode: 0o755 });
  writeFileSync(path.join(bin, "gh"), `#!/usr/bin/env node
const fs=require('node:fs'), path=require('node:path');
const args=process.argv.slice(2), root=process.env.FIXTURE, state=JSON.parse(fs.readFileSync(path.join(root,'state.json'),'utf8'));
fs.appendFileSync(path.join(root,'calls'),JSON.stringify(['gh',...args])+'\\n');
if(args[0]==='release'&&args[1]==='view') console.log(JSON.stringify(args[2].startsWith('desktop-')?state.desktop:state.cli));
else if(args[0]==='api'&&args.join(' ').includes('homebrew-reasonix')) console.log('cask "reasonix" do\\n  version "1.2.3"\\nend');
else process.exit(4);
`, { mode: 0o755 });
  writeFileSync(path.join(bin, "npm"), `#!/usr/bin/env node
const fs=require('node:fs'), path=require('node:path');
const args=process.argv.slice(2), root=process.env.FIXTURE, state=JSON.parse(fs.readFileSync(path.join(root,'state.json'),'utf8'));
fs.appendFileSync(path.join(root,'calls'),JSON.stringify(['npm',...args])+'\\n');
const [spec]=args.slice(1); const name=spec.slice(0,spec.lastIndexOf('@'));
const out={name,version:'1.2.3',reasonixCandidateSha:'${sha}',gitHead:'${sha}','dist.integrity':'sha512-x'};
for (const field of args) if (field.startsWith('dist-tags.')) { const tag=field.slice(10); const value=tag==='latest'?state.latest:tag==='legacy-v1'?state.legacy:undefined; if(value) out[field]=value; }
console.log(JSON.stringify(out));
`, { mode: 0o755 });
  writeFileSync(path.join(bin, "go"), `#!/usr/bin/env node
const args=process.argv.slice(2);
require('node:fs').writeFileSync(args[3], JSON.stringify({version:'v1.2.3',platforms:{'darwin-arm64':{url:'https://dl.reasonix.io/desktop-v1.2.3/x',sig:'s'}}}));
`, { mode: 0o755 });
  const env = { ...process.env, FIXTURE: root, PATH: `${bin}:${process.env.PATH}`, RELEASE_REPOSITORY: "esengine/DeepSeek-Reasonix", RELEASE_VERSION: "1.2.3", CLI_TAG: "v1.2.3", DESKTOP_TAG: "desktop-v1.2.3", RELEASE_EXPECTED_SHA: sha, RELEASE_OPERATION: "publish", VERIFY_PUBLIC_POINTERS: "true", VERIFY_ATTEMPTS: "1", VERIFY_DELAY_SECONDS: "0", RELEASE_LEDGER_OUTPUT: path.join(root, "ledger.json") };
  delete env[SWITCH];
  return {
    run: extra => spawnSync("bash", ["scripts/verify-stable-release-artifacts.sh"], { env: { ...env, ...extra }, encoding: "utf8" }),
    calls: () => { try { return readFileSync(path.join(root, "calls"), "utf8"); } catch { return ""; } },
    ledger: () => JSON.parse(readFileSync(env.RELEASE_LEDGER_OUTPUT, "utf8")),
  };
}

test("postflight with the variable unset still requires npm latest and the Homebrew cask", t => {
  const f = verifier(t), result = f.run({});
  assert.equal(result.status, 0, result.stderr);
  assert.match(f.calls(), /dist-tags\.latest/);
  assert.doesNotMatch(f.calls(), /legacy-v1/);
  assert.match(f.calls(), /homebrew-reasonix/);
  const ledger = f.ledger();
  assert.ok(ledger.surfaces.homebrew);
  assert.ok(ledger.surfaces.npm.packages.every(item => item.latest === "1.2.3" && item.distTag === undefined));
  const stale = verifier(t, { latest: "1.2.2" }).run({});
  assert.notEqual(stale.status, 0);
});

test("postflight with the variable empty or false behaves as unset", t => {
  for (const value of ["", "false"]) {
    const f = verifier(t), result = f.run({ [SWITCH]: value });
    assert.equal(result.status, 0, result.stderr);
    assert.match(f.calls(), /homebrew-reasonix/);
  }
});

test("frozen postflight verifies legacy-v1, ignores latest and the cask, and still verifies Desktop", t => {
  const f = verifier(t, { latest: "2.0.0", legacy: "1.2.3" }), result = f.run({ [SWITCH]: "true" });
  assert.equal(result.status, 0, result.stderr);
  assert.match(f.calls(), /dist-tags\.legacy-v1/);
  assert.doesNotMatch(f.calls(), /dist-tags\.latest/);
  assert.doesNotMatch(f.calls(), /homebrew-reasonix/);
  assert.match(f.calls(), /desktop-v1\.2\.3/);
  const ledger = f.ledger();
  assert.equal(ledger.surfaces.homebrew, undefined);
  assert.ok(ledger.surfaces.stableManifest);
  assert.ok(ledger.surfaces.desktop);
  assert.ok(ledger.surfaces.npm.packages.every(item => item.distTag === "legacy-v1" && item.distTagVersion === "1.2.3"));
});

test("frozen postflight fails when the legacy-v1 tag did not land", t => {
  const result = verifier(t, { legacy: "" }).run({ [SWITCH]: "true" });
  assert.notEqual(result.status, 0);
});

test("the npm-v tag guard survives a freeze; only the latest poll is skipped", () => {
  const step = workflow("release.yml").split("- name: Check npm latest dist-tag freshness")[1].split("\n      - ")[0];
  assert.doesNotMatch(step.split("run:")[0], /\bif:/);
  const tagCheck = step.indexOf("git ls-remote --exit-code origin");
  const freeze = step.indexOf('"${CLI_PUBLISH_FROZEN:-}" = "true"');
  const poll = step.indexOf("npm view reasonix dist-tags.latest");
  assert.ok(tagCheck > 0 && tagCheck < freeze && freeze < poll);
});

test("shell and workflow sides agree on every spelling of the variable", t => {
  const envExpression = expressionAfter("release.yml", "- name: Check npm latest dist-tag freshness\n        env:\n          TAG: ${{ needs.resolve.outputs.tag }}\n          CLI_PUBLISH_FROZEN: ");
  const guard = expressionAfter("release.yml", "- name: Publish prepared Homebrew cask\n        if: ");
  for (const raw of ["true", "TRUE", "True", " true", "true ", "false", "", "1", "yes"]) {
    const shell = String(evaluate(envExpression, { frozen: raw }));
    const workflowFrozen = evaluate(guard, { frozen: raw }) === false;
    assert.equal(shell === "true", workflowFrozen, JSON.stringify(raw));
    const f = verifier(t), result = f.run({ [SWITCH]: shell });
    assert.equal(result.status, 0, result.stderr);
    assert.equal(/homebrew-reasonix/.test(f.calls()), !workflowFrozen, JSON.stringify(raw));
  }
});
