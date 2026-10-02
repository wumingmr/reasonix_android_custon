#!/usr/bin/env node
// Runs the frontend build for one shell. The shell name travels through the
// environment because npm scripts cannot set variables portably on Windows.
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { shellFromEnv } from "./shell-css.mjs";

const shell = shellFromEnv({ REASONIX_SHELL: process.argv[2] ?? "" });
const frontend = fileURLToPath(new URL("..", import.meta.url));
const bundleOnly = process.argv.includes("--bundle-only");
const commands = bundleOnly
  ? [["exec", "vite", "build"], ["exec", "node", "scripts/check-bundle-budget.mjs"]]
  : [["build"]];
let status = 0;
for (const args of commands) {
  const result = spawnSync("pnpm", args, {
    cwd: frontend,
    stdio: "inherit",
    env: { ...process.env, REASONIX_SHELL: shell },
    shell: process.platform === "win32",
  });
  status = result.status ?? 1;
  if (status !== 0) break;
}
process.exit(status);
