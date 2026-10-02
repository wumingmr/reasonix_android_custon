import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { pathToFileURL } from "node:url";
import path from "node:path";

const endpoint = "repos/esengine/DeepSeek-Reasonix/actions/runs/34816299501";
const read = suffix => JSON.parse(execFileSync("gh", ["api", endpoint + suffix], { encoding: "utf8" }));
export function verifyProducer(run, jobs) {
assert.equal(run.repository.full_name, "esengine/DeepSeek-Reasonix");
assert.equal(run.head_sha, "09cdab3866d77c6ff0d007ee61b6aca3128ebe54");
assert.equal(run.head_branch, "main-v2");
assert.equal(run.event, "workflow_dispatch");
assert.equal(run.path, ".github/workflows/release-stable.yml");
assert.equal(run.run_attempt, 1);
assert.equal(run.status, "completed");
for (const platform of ["darwin-arm64", "darwin-amd64", "darwin-universal", "windows-amd64", "windows-arm64", "linux-amd64"]) {
  const matches = jobs.filter(job => job.name === `verify stable SignPath control plane / build (${platform}, preflight)`);
  assert.equal(matches.length, 1);
  assert.equal(matches[0].conclusion, "success");
}
}
if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
verifyProducer(read(""), read("/attempts/1/jobs?per_page=100").jobs);
console.log("Verified the protected producer and all six native build jobs; artifact identity and digests must still pass collection.");
}
