// Inspect the same maintainer credential used for the atomic tag push. A dry
// run cannot prove server-side ruleset authorization, so read the live policy.
import { execFileSync } from "node:child_process";
import { appendFileSync } from "node:fs";
import { pathToFileURL } from "node:url";

const repository = "esengine/DeepSeek-Reasonix";

// Release tags contain no slash after refs/tags/. Unknown pattern syntax is
// treated conservatively: it may include a tag, but cannot exclude one.
function matches(pattern, ref, unknown) {
  if (pattern === "~ALL") return true;
  if (typeof pattern !== "string" || /[\[\]{}\\]/.test(pattern) || pattern.includes("**") || pattern.startsWith("~")) return unknown;
  const expression = pattern.split("").map(char => char === "*" ? "[^/]*" : char === "?" ? "[^/]" : char.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join("");
  return new RegExp(`^${expression}$`).test(ref);
}

export function verifyIdentity({ actor, repo, rulesets, expectedActor, expectedID, version }) {
  if (!/^[A-Za-z0-9][A-Za-z0-9-]*$/.test(expectedActor || "")) throw new Error("Configure RELEASE_TAG_ACTOR with the authorized maintainer login");
  if (!/^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/.test(version)) throw new Error("Invalid release version");
  if (actor.type !== "User" || actor.login?.toLowerCase() !== expectedActor.toLowerCase() || !Number.isSafeInteger(actor.id)) throw new Error("Release tag credential does not identify the configured maintainer");
  if (expectedID && String(actor.id) !== String(expectedID)) throw new Error("Release tag identity changed after preflight");
  if (repo.full_name !== repository || repo.permissions?.push !== true) throw new Error("Release tag identity cannot push to the official repository");
  if (!Array.isArray(rulesets)) throw new Error("Missing repository rulesets");
  const refs = [`v${version}`, `npm-v${version}`, `desktop-v${version}`].map(tag => `refs/tags/${tag}`);
  const checked = [];
  for (const rule of rulesets) {
    if (!["active", "evaluate", "disabled"].includes(rule.enforcement)) throw new Error("Unknown ruleset enforcement");
    if (rule.enforcement !== "active" || !["tag", "push"].includes(rule.target)) continue;
    const conditions = rule.conditions?.ref_name;
    const applies = rule.target === "push" || !conditions || refs.some(ref =>
      (!Array.isArray(conditions.include) || conditions.include.some(pattern => matches(pattern, ref, true))) &&
      !(conditions.exclude || []).some(pattern => matches(pattern, ref, false)));
    if (!applies) continue;
    if (!Array.isArray(rule.rules)) throw new Error(`Cannot inspect ruleset ${rule.id}`);
    // Updates and deletions are never requested. All other rules must explicitly
    // allow this maintainer to bypass; do not guess at server rule semantics.
    if (rule.rules.some(item => !["update", "deletion"].includes(item.type)) && rule.current_user_can_bypass !== "always") {
      throw new Error(`Release tag identity cannot bypass active ruleset ${rule.id}; configure an already-authorized maintainer, not weaker protections`);
    }
    checked.push(rule.id);
  }
  return { actor: actor.login, actorID: String(actor.id), rulesets: checked };
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  if (!process.env.GH_TOKEN) throw new Error("RELEASE_TAG_TOKEN is required; the default Actions token is not a release identity");
  if (process.env.GITHUB_ACTIONS === "true" && (process.env.GITHUB_REPOSITORY !== repository || process.env.GITHUB_REF !== "refs/heads/main-v2" || process.env.GITHUB_REF_PROTECTED !== "true")) throw new Error("Release identity checks require protected official main-v2");
  const api = endpoint => JSON.parse(execFileSync("gh", ["api", endpoint], { encoding: "utf8", stdio: ["ignore", "pipe", "inherit"] }));
  const listed = JSON.parse(execFileSync("gh", ["api", "--paginate", "--slurp", `repos/${repository}/rulesets?includes_parents=true&per_page=100`], { encoding: "utf8" })).flat();
  const result = verifyIdentity({
    actor: api("user"), repo: api(`repos/${repository}`),
    rulesets: listed.filter(rule => ["tag", "push"].includes(rule.target) && rule.enforcement === "active").map(rule => api(`repos/${repository}/rulesets/${rule.id}?includes_parents=true`)),
    expectedActor: process.env.RELEASE_TAG_ACTOR, expectedID: process.env.RELEASE_TAG_ACTOR_ID, version: process.argv[2],
  });
  console.log(JSON.stringify(result));
  if (process.env.GITHUB_OUTPUT) appendFileSync(process.env.GITHUB_OUTPUT, `actor_id=${result.actorID}\n`);
}
