// Run: tsx src/__tests__/composer-workspace-frame-css.test.ts
//
// Contract: the new-session workspace selector and composer share one outline.
// The child surfaces provide only the internal separator so focus/running
// accents cannot begin halfway down the combined control.

import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const testDir = dirname(fileURLToPath(import.meta.url));
const styles = [
  readFileSync(resolve(testDir, "../styles.css"), "utf8"),
  readFileSync(resolve(testDir, "../components/ComposerWorkspaceContextBar.css"), "utf8"),
].join("\n").replace(/\/\*[\s\S]*?\*\//g, "");
const composerSource = readFileSync(resolve(testDir, "../components/Composer.tsx"), "utf8");

let passed = 0;
let failed = 0;

function ok(value: unknown, label: string) {
  if (value) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed += 1;
  } else {
    process.stdout.write(`  FAIL  ${label}\n`);
    failed += 1;
  }
}

function matchingBlocks(selector: string): string[] {
  const blocks: string[] = [];
  const rule = /([^{}]+)\{([^{}]*)\}/g;
  let match: RegExpExecArray | null;
  while ((match = rule.exec(styles)) !== null) {
    const selectors = match[1].split(",").map((part) => part.trim());
    if (selectors.includes(selector)) blocks.push(match[2]);
  }
  return blocks;
}

function finalDeclaration(selector: string, property: string): string | undefined {
  let value: string | undefined;
  for (const block of matchingBlocks(selector)) {
    const declaration = new RegExp(`(?:^|;)\\s*${property}\\s*:\\s*([^;]+)`, "g");
    let match: RegExpExecArray | null;
    while ((match = declaration.exec(block)) !== null) value = match[1].trim();
  }
  return value;
}

console.log("\ncomposer workspace frame css");

ok(composerSource.includes("composer-workspace-frame--context"), "workspace context mounts a shared frame");
ok(composerSource.includes("!workspaceContext && running"), "plain composers keep ownership of their existing running accent");
ok(finalDeclaration(".composer-workspace-frame--context", "border-radius") === "16px", "shared frame owns the complete rounded outline");
ok(finalDeclaration(".composer-workspace-frame--context .composer-workspace-bar", "border") === "0", "workspace bar does not draw a competing outer border");
ok(finalDeclaration(".composer-workspace-frame--context .composer-workspace-bar", "border-bottom") === "1px solid var(--border-soft)", "workspace bar keeps only the internal separator");
ok(finalDeclaration(":root[data-theme-style] .composer-workspace-frame--context .composer-card", "border") === "0", "theme cascade cannot restore the composer card outer border");
ok(finalDeclaration(":root[data-theme-style] .composer-workspace-frame--context .composer-card", "border-radius") === "0 0 15px 15px", "theme cascade cannot restore the composer card top corners");
ok(finalDeclaration(":root[data-theme-style] .composer-workspace-frame--context .composer-card::before", "display") === "none", "composer card focus outline is handed to the shared frame");
ok(finalDeclaration(":root[data-theme-style] .composer-workspace-frame--context .composer-card::after", "display") === "none", "composer card animated outline is handed to the shared frame");

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
