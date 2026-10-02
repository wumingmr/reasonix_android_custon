import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import os from "node:os";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const read = (relativePath) =>
  fs.readFileSync(path.join(repoRoot, relativePath), "utf8");

const frontendPackage = JSON.parse(read("desktop/frontend/package.json"));
const ciWorkflow = read(".github/workflows/ci.yml");
const releaseWorkflow = read(".github/workflows/release-desktop.yml");
const readme = read("README.md");
const desktopReadme = read("desktop/README.md");
const desktopBuildScript = read("scripts/desktop-build.sh");

// Execute the production shell wrapper to check the identity passed to packaging.
const packageShell = desktopBuildScript.match(/^package_shell\(\) \{\n[\s\S]*?^\}/m)?.[0];
assert.ok(packageShell, "desktop builds must define package_shell");
const sourceSha = "a".repeat(40);
const identityProbe = spawnSync("bash", ["-c", `${packageShell}\nnode() { printf '%s' "$REASONIX_COMMIT"; }\npackage_shell`], {
  encoding: "utf8",
  env: { ...process.env, ROOT: "/fixture", PLATFORM: "windows/amd64", VERSION: "v0.0.0-ci", CHANNEL: "canary",
    SOURCE_SHA: sourceSha, GIT_COMMIT: sourceSha.slice(0, 12), BUILD_TIME_UTC: "2026-01-01T00:00:00Z" },
});
assert.ifError(identityProbe.error);
assert.equal(identityProbe.status, 0, identityProbe.stderr);
assert.equal(identityProbe.stdout.trim().split("\n").at(-1), sourceSha, "packaged identity must retain the full source SHA");

const jobBody = (workflow, jobName) => {
  const lines = workflow.split("\n");
  const start = lines.findIndex((line) => line === `  ${jobName}:`);
  assert.notEqual(start, -1, `workflow must define the ${jobName} job`);
  const nextJob = lines
    .slice(start + 1)
    .findIndex((line) => /^  [a-zA-Z0-9_-]+:$/.test(line));
  const end = nextJob === -1 ? lines.length : start + 1 + nextJob;
  return lines.slice(start, end).join("\n");
};

const nodeVersions = (workflow) =>
  [...workflow.matchAll(/node-version(-file)?:\s*["']?([^\s"']+)/g)].map(
    (match) => match[1] ? read(match[2]).trim() : match[2],
  );

const pinnedNodeVersion = read(".node-version").trim();
assert.match(pinnedNodeVersion, /^24\.\d+\.\d+$/);
assert.equal(read("desktop/frontend/.nvmrc").trim(), "24");
assert.equal(frontendPackage.engines?.node, ">=24");
assert.equal(frontendPackage.engines?.pnpm, ">=10 <11");
assert.ok(
  !fs.existsSync(path.join(repoRoot, "desktop/wails.json")),
  "desktop/wails.json must be retired with the Wails shell",
);

for (const jobName of ["desktop-prepare", "desktop-go", "desktop-frontend", "desktop-browser-group", "desktop-macos", "desktop-windows"]) {
  assert.deepEqual(nodeVersions(jobBody(ciWorkflow, jobName)), [pinnedNodeVersion]);
}

const releaseNodeVersions = nodeVersions(releaseWorkflow);
assert.ok(releaseNodeVersions.length > 0, "release workflow must set up Node");
assert.deepEqual(new Set(releaseNodeVersions), new Set([pinnedNodeVersion]));

for (const [name, workflow] of [
  ["CI", ciWorkflow],
  ["release", releaseWorkflow],
]) {
  const lines = workflow.split("\n");
  const pnpmVersions = lines.flatMap((line, index) => {
    if (!line.includes("pnpm/action-setup@")) return [];
    const block = lines.slice(index, index + 5).join("\n");
    return [block.match(/version:\s*(\d+)/)?.[1] ?? "missing"];
  });
  assert.ok(pnpmVersions.length > 0, `${name} workflow must set up pnpm`);
  assert.deepEqual(new Set(pnpmVersions), new Set(["10"]));
}

for (const [name, content] of [
  ["README.md", readme],
  ["desktop/README.md", desktopReadme],
]) {
  assert.match(content, /npm i(?:nstall)? -g pnpm@10/);
  assert.doesNotMatch(
    content,
    /wails/i,
    `${name} must not reference the retired Wails toolchain`,
  );
}

assert.match(readme, /#### CLI/);
assert.match(readme, /#### Desktop/);

// The desktop build is the Electron packaging entrypoint: it must regenerate
// the shell/service contract and fail on drift before compiling anything.
assert.match(
  desktopBuildScript,
  /go run \. -emit-contract frontend\/src\/generated/,
  "desktop builds must regenerate the host contract",
);
// Verify the build guard's behavior, not its choice of git or diff syntax.
// A locally edited but current contract is valid; regeneration drift is not.
const guardStart = desktopBuildScript.indexOf('echo "==> desktop host contract drift check"');
const guardEnd = desktopBuildScript.indexOf("# The packaging script", guardStart);
assert.ok(guardStart >= 0 && guardEnd > guardStart, "contract guard must precede packaging");
const contractGuard = desktopBuildScript.slice(guardStart, guardEnd);
const fixture = fs.mkdtempSync(path.join(os.tmpdir(), "reasonix-contract-guard-"));
try {
  for (const mode of ["current", "changed", "added", "removed", "generator-failed"]) {
    const cwd = path.join(fixture, mode);
    const generated = path.join(cwd, "frontend/src/generated");
    fs.mkdirSync(generated, { recursive: true });
    fs.writeFileSync(path.join(generated, "contract.json"), '{"version":10}\n');
    const script = `set -euo pipefail
go() {
  case "$GUARD_TEST_MODE" in
    current) : ;;
    changed) echo '{"version":11}' > frontend/src/generated/contract.json ;;
    added) echo '{}' > frontend/src/generated/new.json ;;
    removed) rm frontend/src/generated/contract.json ;;
    generator-failed) return 42 ;;
  esac
}
${contractGuard}`;
    const result = spawnSync("bash", ["-c", script], {
      cwd, encoding: "utf8", env: { ...process.env, GUARD_TEST_MODE: mode, TMPDIR: fixture },
    });
    assert.ifError(result.error);
    if (mode === "current") {
      assert.equal(result.status, 0, `current uncommitted contract rejected: ${result.stderr}`);
    } else {
      assert.notEqual(result.status, 0, `contract guard accepted ${mode}`);
    }
  }
} finally {
  fs.rmSync(fixture, { recursive: true, force: true });
}
// The release channel now rides in the Go service ldflags (the shell reads
// the same identity from resources/build.json written by package.mjs).
assert.match(
  desktopBuildScript,
  /service_ldflags="-X main\.version=\$VERSION -X main\.channel=\$CHANNEL/,
  "desktop builds must link the release channel into the Go service",
);
assert.match(
  desktopBuildScript,
  /\[ "\$os" = "windows" \] && service_ldflags="\$service_ldflags -H windowsgui"/,
  "Windows desktop builds must link the Go service as a GUI-subsystem image",
);
assert.match(
  desktopBuildScript,
  /GOOS="\$os" GOARCH="\$arch" go build -trimpath -ldflags="-s -w \$service_ldflags" -o "\$service_out"/,
  "desktop service builds must consume the platform-specific linker flags",
);
const windowsJob = jobBody(ciWorkflow, "desktop-windows");
assert.match(
  windowsJob,
  /go build -trimpath -ldflags "-s -w -H windowsgui -X main\.version=v0\.0\.0-ci -X main\.channel=canary" -o build\/bin\/reasonix-desktop\.exe \./,
  "Windows native startup CI must build the service as a GUI-subsystem image",
);
assert.match(
  windowsJob,
  /node \.\.\/scripts\/verify-windows-gui-subsystem\.mjs build\/bin\/reasonix-desktop\.exe/,
  "Windows native startup CI must verify the service PE subsystem",
);
// The shell is packaged through the Electron packaging script, never wails build.
assert.match(
  desktopBuildScript,
  /node "\$ROOT\/desktop\/packaging\/package\.mjs" "\$PLATFORM" "\$VERSION" "\$CHANNEL"/,
  "desktop builds must package the shell through desktop/packaging/package.mjs",
);
assert.match(
  desktopBuildScript,
  /darwin\) report_bundle="\$ROOT\/desktop\/build\/candidate\/darwin-\$\{arch\}\/\$\{APPNAME\}\.app"/,
  "macOS size reports must inspect the retained candidate instead of the deleted staging app",
);
assert.doesNotMatch(desktopBuildScript, /wails build/);
assert.doesNotMatch(
  desktopBuildScript,
  /github\.com\/wailsapp\/wails\/v2\/cmd\/wails@/,
);
// darwin/universal still ships one fat binary per Go artifact.
assert.match(
  desktopBuildScript,
  /lipo -create "\$service_tmp\/amd64" "\$service_tmp\/arm64" -output "\$service_out"/,
  "darwin universal builds must lipo the desktop service",
);
// Windows keeps one canonical SignPath payload: signing-files.txt enumerates
// every PE file, then package-windows-desktop.sh rebuilds from the payload.
assert.match(
  desktopBuildScript,
  /node "\$ROOT\/desktop\/packaging\/signing-files\.mjs" "\$payload_dir"/,
  "windows builds must enumerate the signing payload",
);
assert.match(
  desktopBuildScript,
  /VERSION="\$VERSION" "\$ROOT\/scripts\/package-windows-desktop\.sh" "\$arch" "\$payload_dir"/,
  "windows builds must package from the signing payload",
);

console.log("desktop build contract: PASS");
