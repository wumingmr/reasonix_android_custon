import { readFileSync } from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";

const VERSION_RE = /version "(\d+)\.(\d+)\.(\d+)"/;

function compareVersion(left, right) {
  const a = left.map(Number); const b = right.map(Number);
  for (let i = 0; i < 3; i += 1) if (a[i] !== b[i]) return a[i] > b[i] ? 1 : -1;
  return 0;
}

export function decideCaskUpdate(existing, candidate) {
  if (existing === candidate) return "reuse";
  const current = existing.match(VERSION_RE);
  const next = candidate.match(VERSION_RE);
  if (!next) throw new Error("candidate cask has no canonical version");
  if (current && compareVersion(next.slice(1), current.slice(1)) < 0) throw new Error("refusing to move Homebrew cask backwards");
  if (current && compareVersion(next.slice(1), current.slice(1)) === 0) throw new Error("existing Homebrew version has conflicting content");
  return "publish";
}

async function request(url, token, options = {}) {
  const response = await fetch(url, {
    ...options,
    headers: { Accept: "application/vnd.github+json", Authorization: `Bearer ${token}`, "Content-Type": "application/json", "X-GitHub-Api-Version": "2022-11-28", ...options.headers },
  });
  if (!response.ok) throw new Error(`GitHub API ${response.status}: ${await response.text()}`);
  return response.json();
}

async function publish(file) {
  const token = process.env.HOMEBREW_TAP_TOKEN;
  if (!token) throw new Error("HOMEBREW_TAP_TOKEN is required");
  const candidate = readFileSync(file, "utf8");
  const api = "https://api.github.com/repos/esengine/homebrew-reasonix/contents/Casks/reasonix.rb";
  const current = await request(api, token);
  const existing = Buffer.from(current.content, "base64").toString("utf8");
  if (decideCaskUpdate(existing, candidate) === "reuse") return;
  await request(api, token, {
    method: "PUT",
    body: JSON.stringify({
      message: `chore: update Reasonix to ${candidate.match(VERSION_RE).slice(1).join(".")}`,
      content: Buffer.from(candidate).toString("base64"),
      sha: current.sha,
      branch: "main",
      committer: { name: "reasonix", email: "reasonix@deepseek.com" },
    }),
  });
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  if (!process.argv[2]) throw new Error("usage: publish-homebrew-cask.mjs CASK_FILE");
  await publish(process.argv[2]);
}
