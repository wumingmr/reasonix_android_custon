import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";
import { verifyProducer } from "./verify-manual-desktop-producer.mjs";

function fixture() {
  return [{ repository: { full_name: "esengine/DeepSeek-Reasonix" }, head_sha: "09cdab3866d77c6ff0d007ee61b6aca3128ebe54", head_branch: "main-v2", event: "workflow_dispatch", path: ".github/workflows/release-stable.yml", run_attempt: 1, status: "completed" },
    ["darwin-arm64", "darwin-amd64", "darwin-universal", "windows-amd64", "windows-arm64", "linux-amd64"].map(platform => ({ name: `verify stable SignPath control plane / build (${platform}, preflight)`, conclusion: "success" }))];
}
test("accepts only the protected producer with a complete successful matrix", () => {
  verifyProducer(...fixture());
  for (const field of ["head_sha", "head_branch", "event", "path", "run_attempt", "status"]) {
    const [run, jobs] = fixture(); run[field] = "different";
    assert.throws(() => verifyProducer(run, jobs));
  }
  const [run, jobs] = fixture();
  assert.throws(() => verifyProducer(run, jobs.slice(1)));
  assert.throws(() => verifyProducer(run, [...jobs, jobs[0]]));
  jobs[0].conclusion = "failure";
  assert.throws(() => verifyProducer(run, jobs));
});
test("artifact recovery installs smoke dependencies and retains collection identity checks", () => {
  const workflow = readFileSync(new URL("../.github/workflows/release-desktop.yml", import.meta.url), "utf8");
  const intel = workflow.split("  mac-universal-intel:")[1].split("  publish:")[0];
  assert.ok(intel.indexOf("pnpm --dir desktop install --frozen-lockfile") < intel.indexOf("node desktop/packaging/smoke.mjs"));
  assert.ok(intel.includes("needs.signing-contract.result == 'success'"));
  assert.ok(workflow.includes("GITHUB_RUN_ID=34816299501 GITHUB_RUN_ATTEMPT=1 node release-control/scripts/desktop-release-artifacts.mjs collect"));
  assert.ok(workflow.includes("inputs.reuse_manual_artifacts && '09cdab3866d77c6ff0d007ee61b6aca3128ebe54' || inputs.candidate_control_sha || github.workflow_sha"));
});
