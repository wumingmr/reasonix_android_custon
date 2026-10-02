import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const script = fileURLToPath(new URL("./package-desktop-dmg.sh", import.meta.url));
for (const scenario of ["no-output", "invalid-output", "customization-error", "success"]) {
  test(`fresh DMG publication: ${scenario}`, t => {
    const root = mkdtempSync(join(tmpdir(), "reasonix-dmg-"));
    t.after(() => rmSync(root, { recursive: true, force: true }));
    const app = join(root, "Reasonix.app");
    const bin = join(root, "bin");
    const dist = join(root, "dist");
    for (const dir of [app, bin, dist]) mkdirSync(dir);
    writeFileSync(join(app, "build.json"), "current-build");
    const output = join(dist, "Reasonix.dmg");
    writeFileSync(output, "old-build");
    writeFileSync(join(bin, "create-dmg"), `#!/usr/bin/env bash
set -eu
while [ "$#" -gt 2 ]; do shift; done
[ "$1" != "$OUTPUT" ]
[ ! -e "$1" ]
[ "$(cat "$2/Reasonix.app/build.json")" = current-build ]
case "$SCENARIO" in
  no-output) exit 1 ;;
  invalid-output) printf invalid > "$1" ;;
  customization-error) printf current-build > "$1"; exit 1 ;;
  success) printf current-build > "$1" ;;
esac
`, { mode: 0o755 });
    writeFileSync(join(bin, "hdiutil"), `#!/usr/bin/env bash
set -eu
[ "$1" = verify ]
[ "$(cat "$2")" = current-build ]
`, { mode: 0o755 });
    const result = spawnSync("bash", [script, app, output, "Reasonix"], {
      encoding: "utf8",
      env: { ...process.env, PATH: `${bin}:${process.env.PATH}`, OUTPUT: output, SCENARIO: scenario },
    });
    const succeeds = ["customization-error", "success"].includes(scenario);
    assert.equal(result.status === 0, succeeds, result.stderr);
    assert.equal(readFileSync(output, "utf8"), succeeds ? "current-build" : "old-build");
    assert.deepEqual(readdirSync(dist), ["Reasonix.dmg"]);
  });
}
