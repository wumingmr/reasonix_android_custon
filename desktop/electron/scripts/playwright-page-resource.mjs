import { readFile, copyFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { runInNewContext } from "node:vm";
import ts from "typescript";

export async function playwrightPageResource(dist) {
  const require = createRequire(import.meta.url);
  const core = createRequire(require.resolve("playwright/package.json")).resolve("playwright-core/package.json");
  const version = JSON.parse(await readFile(core, "utf8")).version;
  if (version !== "1.62.1") throw new Error(`Unqualified Playwright injected runtime version: ${version}`);
  const file = ts.createSourceFile("coreBundle.js", await readFile(join(dirname(core), "lib/coreBundle.js"), "utf8"), ts.ScriptTarget.Latest, true);
  const candidates = [];
  function visit(node) {
    if (ts.isStringLiteral(node) && node.text.includes("injectedScript_exports") && node.text.includes("generateAriaTree")) candidates.push(node.text);
    ts.forEachChild(node, visit);
  }
  visit(file);
  if (candidates.length !== 1) throw new Error("Playwright injected resource export contract changed");
  let source = candidates[0];
  // 1.62.1 serializes text-input values. Never expose password values, including
  // an explicitly role=textbox password. This reviewed adapter is version bound.
  const valueCondition = 'element.type !== "checkbox" && element.type !== "radio" && element.type !== "file"';
  if (source.split(valueCondition).length !== 2) throw new Error("Playwright password redaction contract changed");
  source = source.replace(valueCondition, `${valueCondition} && element.type !== "password"`);
  // The generated export table contains getter functions, not constructors.
  source += "\nmodule.exports = { InjectedScript: module.exports.InjectedScript() };\n";
  const module = { exports: {} };
  runInNewContext(source, { module }, { timeout: 1000 });
  const prototype = module.exports.InjectedScript?.prototype;
  for (const method of ["ariaSnapshotWithRefs", "parseSelector", "querySelectorAll", "elementState"]) {
    if (typeof prototype?.[method] !== "function") throw new Error(`Playwright contract missing ${method}`);
  }
  await copyFile(join(dirname(core), "LICENSE"), join(dist, "browser-playwright-LICENSE"));
  await copyFile(join(dirname(core), "NOTICE"), join(dist, "browser-playwright-NOTICE"));
  return { version, source };
}
