import assert from "node:assert/strict";
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";

// Prevent newly added inline error labels from bypassing the shared presenter.
// Raw diagnostics in explicit detail/code views remain verbatim by design.
const root = join(dirname(fileURLToPath(import.meta.url)), "..");
function files(directory: string): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap(entry =>
    entry.name === "__tests__" ? [] : entry.isDirectory() ? files(join(directory, entry.name)) : [join(directory, entry.name)]);
}
const violations: string[] = [];
for (const file of files(root).filter(name => name.endsWith(".tsx") && !name.endsWith(".test.tsx"))) {
  const source = ts.createSourceFile(file, readFileSync(file, "utf8"), ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  function visit(node: ts.Node) {
    if (ts.isJsxExpression(node) && node.expression && ts.isJsxElement(node.parent)) {
      const expression = node.expression.getText(source);
      const tag = node.parent.openingElement.tagName.getText(source);
      if (/^(?:[a-zA-Z_$][\w$]*\??\.)*(?:\w*(?:Err|Error)|err|error)$/.test(expression)
        && /^[a-z]/.test(tag) && !["pre", "code"].includes(tag)) {
        violations.push(`${relative(root, file)}:${source.getLineAndCharacterOfPosition(node.pos).line + 1}: ${expression}`);
      }
    }
    ts.forEachChild(node, visit);
  }
  visit(source);
}
assert.deepEqual(violations, [], "User-visible errors must go through ErrorMessage; raw details belong in explicit diagnostic views");
console.log("PASS error display contract: inline error surfaces use the shared presenter");
