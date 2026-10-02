import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";
import { deflateRawSync } from "node:zlib";
import {
  checkEntryModes,
  checkStaticGoMembers,
  elfInterpreter,
  checkMembers,
  displayVersion,
  inferArtifactKind,
  listZipEntries,
  readZipMember,
  nsisProjectDefines,
  numericVersion,
  packagerOptions,
  parseSigningFileList,
  parseTarget,
  parseVerboseListing,
  PRODUCT,
  readProductIdentity,
  releaseVersions,
  requiredMembers,
  runBuildScript,
  sanitizeShellPackageJson,
  shellIgnore,
  signingFileList,
  versionTag,
  validateMacServiceLink,
  WINDOWS_FLAT_PAYLOAD,
} from "./lib.mjs";

const desktop = dirname(dirname(fileURLToPath(import.meta.url)));
const read = (path) => readFileSync(join(desktop, path), "utf8");
const identity = { projectName: "reasonix-desktop", companyName: "Reasonix", productName: "Reasonix", copyright: "Copyright © 2026 Reasonix Contributors" };

test("build scripts preserve paths, arguments and environment without shell encoding", (t) => {
  const directory = mkdtempSync(join(tmpdir(), "reasonix build & 中文 "));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  mkdirSync(join(directory, "scripts"));
  const output = join(directory, "result.json");
  writeFileSync(join(directory, "scripts", "fixture.mjs"), `
    import { writeFileSync } from "node:fs";
    writeFileSync(process.env.REASONIX_BUILD_TEST_OUTPUT, JSON.stringify({
      args: process.argv.slice(2), cwd: process.cwd(), channel: process.env.REASONIX_CHANNEL,
    }));
  `);
  const args = ["", "a b", 'a"b', "C:\\build path\\", 'C:\\path\\"quoted"\\', "a&b|c<d>e^f%PATH%!x!", "$(echo unwanted)", "中文"];
  runBuildScript(directory, "fixture.mjs", args, { REASONIX_CHANNEL: "preview", REASONIX_BUILD_TEST_OUTPUT: output });
  const actual = JSON.parse(readFileSync(output, "utf8"));
  assert.deepEqual(actual.args, args);
  assert.equal(actual.channel, "preview");
  assert.equal(readFileSync(join(actual.cwd, "result.json"), "utf8"), readFileSync(output, "utf8"));
  writeFileSync(join(directory, "scripts", "failure.mjs"), "process.exit(17);\n");
  assert.throws(() => runBuildScript(directory, "failure.mjs"), /failure\.mjs exited with 17/);
});

test("targets map Go platform names onto packager platform and arch", () => {
  assert.deepEqual(parseTarget("darwin/universal"), { os: "darwin", arch: "universal", packagerPlatform: "darwin", packagerArch: "universal", spec: "darwin/universal", key: "darwin-universal" });
  assert.equal(parseTarget("windows/amd64").packagerArch, "x64");
  assert.equal(parseTarget("windows/arm64").packagerPlatform, "win32");
  assert.equal(parseTarget("linux/amd64").key, "linux-amd64");
  assert.throws(() => parseTarget("windows/universal"), /unsupported target/);
  assert.throws(() => parseTarget("darwin"), /unsupported target/);
});

test("versions keep the full tag for identity and strip it for OS resources", () => {
  assert.deepEqual(releaseVersions("v1.38.9-2"), { canonical: "v1.38.9-2", display: "1.38.9-2", resource: "1.38.9" });
  assert.deepEqual(releaseVersions("v1.2.3-preview.42"), { canonical: "v1.2.3-preview.42", display: "1.2.3-preview.42", resource: "1.2.3" });
  assert.equal(numericVersion("v1.2.3"), "1.2.3");
  assert.equal(numericVersion("v1.2.3-rc.1"), "1.2.3");
  assert.equal(displayVersion("v1.2.3-rc.1"), "1.2.3-rc.1");
  assert.equal(numericVersion("v0.0.0-local"), "0.0.0");
  assert.equal(versionTag("v1.20.0-preview.42"), "v1.20.0-preview.42");
  for (const bad of ["1.2.3", "v1.2", "v01.2.3", "v1.2.3+meta", ""]) assert.throws(() => numericVersion(bad), /version must look like/);
});

test("the product identity is a frozen constant and keeps the Wails-era bundle id", () => {
  const product = readProductIdentity();
  assert.equal(product.productName, "Reasonix");
  assert.equal(product.projectName, "reasonix-desktop");
  assert.equal(product.companyName, "Reasonix");
  assert.match(product.copyright, /Reasonix Contributors/);
  assert.equal(PRODUCT.bundleId, "com.wails.reasonix-desktop");
});

test("only the shell bundle and its package.json enter the asar", () => {
  for (const kept of ["", "/package.json", "/dist", "/dist/main.cjs", "/dist/preload.cjs", "/dist/desktopContract.json", "/dist/guestPreload.cjs"]) {
    assert.equal(shellIgnore(kept), false, kept);
  }
  for (const dropped of ["/dist/main.cjs.map", "/src", "/src/main/index.ts", "/node_modules", "/node_modules/electron", "/scripts/build.mjs", "/tsconfig.json", "/README.md", "/artifacts"]) {
    assert.equal(shellIgnore(dropped), true, dropped);
  }
});

test("package.json uses the numeric native version; build.json owns the full release identity", () => {
  const pkg = sanitizeShellPackageJson(
    { name: "reasonix-desktop-shell", private: true, version: "0.0.0", type: "module", main: "dist/main.cjs", description: "shell", scripts: { build: "x" }, devDependencies: { electron: "44.2.0" }, engines: { node: ">=24" } },
    { version: "v1.2.3-rc.1", productName: "Reasonix" },
  );
  assert.deepEqual(pkg, { name: "reasonix-desktop-shell", description: "shell", main: "dist/main.cjs", type: "module", productName: "Reasonix", version: "1.2.3" });
});

test("packager options pin the product identity and layout for every target", () => {
  const common = { version: "v1.2.3-rc.1", identity, root: "/repo/desktop", electronVersion: "44.2.0", extraResources: ["/tmp/app", "/tmp/icons", "/tmp/build.json"] };
  const mac = packagerOptions({ ...common, target: parseTarget("darwin/universal"), icon: "/repo/desktop/build/darwin/icon.icns" });
  assert.equal(mac.dir, join("/repo/desktop", "electron"));
  assert.equal(mac.name, "Reasonix");
  assert.equal(mac.executableName, "Reasonix");
  assert.equal(mac.platform, "darwin");
  assert.equal(mac.arch, "universal");
  assert.equal(mac.appBundleId, "com.wails.reasonix-desktop");
  assert.equal(mac.appVersion, "1.2.3");
  assert.equal(mac.buildVersion, "1.2.3");
  assert.equal(mac.appCopyright, identity.copyright);
  assert.equal(mac.asar, true);
  assert.equal(mac.prune, true);
  assert.equal(mac.overwrite, true);
  assert.equal(mac.icon, "/repo/desktop/build/darwin/icon.icns");
  assert.deepEqual(mac.extraResource, common.extraResources);
  assert.equal(mac.ignore, shellIgnore);
  assert.equal(mac.win32metadata, undefined);

  const win = packagerOptions({ ...common, target: parseTarget("windows/arm64"), icon: "/repo/desktop/build/windows/icon.ico" });
  assert.equal(win.platform, "win32");
  assert.equal(win.arch, "arm64");
  assert.deepEqual(win.win32metadata, { CompanyName: "Reasonix", FileDescription: "Reasonix", ProductName: "Reasonix", InternalName: "Reasonix", OriginalFilename: "Reasonix.exe" });

  const linux = packagerOptions({ ...common, target: parseTarget("linux/amd64") });
  assert.equal(linux.platform, "linux");
  assert.equal(linux.arch, "x64");
  assert.equal("icon" in linux, false);
});

test("NSIS project defines replace the Wails-generated INFO_* values", () => {
  const defines = nsisProjectDefines(identity, "v1.38.9-2");
  assert.ok(defines.startsWith("﻿"), "UTF-8 BOM for makensis");
  assert.match(defines, /!define INFO_PROJECTNAME "reasonix-desktop"\r\n/);
  assert.match(defines, /!define INFO_COMPANYNAME "Reasonix"\r\n/);
  assert.match(defines, /!define INFO_PRODUCTNAME "Reasonix"\r\n/);
  assert.match(defines, /!define INFO_PRODUCTVERSION "1\.38\.9"\r\n/);
  assert.match(defines, /!define REASONIX_DISPLAY_VERSION "1\.38\.9-2"\r\n/);
  assert.match(defines, /!define INFO_COPYRIGHT "Copyright © 2026 Reasonix Contributors"\r\n/);
  assert.match(defines, /!define REASONIX_VERSION_TAG "v1\.38\.9-2"\r\n/);
});

test("signing files are every PE file, sorted, deduplicated and slash-normalised", () => {
  const files = signingFileList([
    "reasonix-desktop.exe",
    "app\\Reasonix.exe",
    "app/ffmpeg.dll",
    "app/resources/app.asar",
    "app/LICENSE",
    "app/vk_swiftshader_icd.json",
    "app/d3dcompiler_47.DLL",
    "./reasonix-uninstall.exe",
    "reasonix-desktop.exe",
    "reasonix-payload.json",
  ]);
  assert.deepEqual(files, ["app/Reasonix.exe", "app/d3dcompiler_47.DLL", "app/ffmpeg.dll", "reasonix-desktop.exe", "reasonix-uninstall.exe"]);
  assert.deepEqual(parseSigningFileList("# comment\r\napp/Reasonix.exe\n\n reasonix-cli.exe \n"), ["app/Reasonix.exe", "reasonix-cli.exe"]);
  assert.deepEqual([...WINDOWS_FLAT_PAYLOAD], ["reasonix-desktop.exe", "reasonix-guard.exe", "reasonix-launcher.exe", "reasonix-update-helper.exe", "reasonix-cli.exe", "reasonix-uninstall.exe"]);
});

test("required members cover every artifact and the checks report gaps", () => {
  const macEntries = requiredMembers("darwin-zip").map(String);
  assert.deepEqual(checkMembers([...macEntries, "Reasonix.app/", "Reasonix.app/Contents/"], "darwin-zip"), { missing: [], forbidden: [] });
  assert.deepEqual(checkMembers(macEntries.slice(1), "darwin-zip").missing, [macEntries[0]]);
  assert.deepEqual(checkMembers([...macEntries, "Reasonix.app/Contents/MacOS/reasonix-guard"], "darwin-zip").forbidden, ["Reasonix.app/Contents/MacOS/reasonix-guard"]);
  assert.deepEqual(checkMembers([...macEntries, "Reasonix.app/Contents/Resources/main.cjs.map"], "darwin-zip").forbidden, [String(/(^|\/)(?:[^/]+\.map|__tests__|testdata|\.cache|coverage|npm-debug\.log|pnpm-debug\.log|yarn-error\.log)(?:$|\/)/)]);
  assert.ok(macEntries.includes("Reasonix.app/Contents/MacOS/reasonix-desktop"));
  assert.ok(macEntries.includes("Reasonix.app/Contents/Resources/service/reasonix"));
  assert.ok(macEntries.includes("Reasonix.app/Contents/Resources/service/reasonix-desktop"));
  assert.deepEqual(checkMembers(requiredMembers("darwin-app-dir").map(String), "darwin-app-dir").missing, []);

  const portable = [
    "Reasonix.exe", "reasonix-cli.exe", "current.json",
    "versions/v1.2.3-rc.1/reasonix-desktop.exe", "versions/v1.2.3-rc.1/reasonix-update-helper.exe", "versions/v1.2.3-rc.1/reasonix-cli.exe",
    "versions/v1.2.3-rc.1/app/Reasonix.exe", "versions/v1.2.3-rc.1/app/resources/bin/reasonix-cli-launcher.exe", "versions/v1.2.3-rc.1/app/resources/app.asar", "versions/v1.2.3-rc.1/app/resources/app/index.html", "versions/v1.2.3-rc.1/app/resources/build.json",
  ];
  assert.deepEqual(checkMembers(portable, "windows-portable-zip"), { missing: [], forbidden: [] });
  assert.deepEqual(checkMembers([...portable, "reasonix-launcher.exe"], "windows-portable-zip").forbidden, ["reasonix-launcher.exe"]);
  assert.deepEqual(checkMembers([...portable, "reasonix-launcher.exe"], "windows-portable-zip", "legacy-dual"), { missing: [], forbidden: [] });
  assert.deepEqual(checkMembers(portable, "windows-portable-zip", "legacy-dual").missing, ["reasonix-launcher.exe"]);
  assert.deepEqual(checkMembers([...portable, "unexpected.EXE"], "windows-portable-zip").forbidden, ["unexpected.EXE"]);
  assert.throws(() => checkMembers(portable, "windows-portable-zip", "auto"), /unknown Windows portable layout/);
  assert.deepEqual(checkMembers(portable.filter((name) => !name.endsWith("app/Reasonix.exe")), "windows-portable-zip").missing, [String(/^versions\/v[^/]+\/app\/Reasonix\.exe$/)]);
  assert.deepEqual(checkMembers([...portable, "reasonix-guard.exe"], "windows-portable-zip").forbidden, ["reasonix-guard.exe"]);

  const winApp = ["Reasonix.exe", "ffmpeg.dll", "libEGL.dll", "libGLESv2.dll", "resources.pak", "icudtl.dat", "locales\\en-US.pak", "resources\\app.asar", "resources\\app\\index.html", "resources\\build.json", "resources\\icons\\appicon.png", "resources\\icons\\trayTemplate.png", "resources\\icons\\trayTemplate@2x.png"];
  assert.deepEqual(checkMembers(winApp, "windows-app-dir"), { missing: [], forbidden: [] });

  const tar = requiredMembers("linux-tar").map(String);
  assert.deepEqual(checkMembers(tar, "linux-tar"), { missing: [], forbidden: [] });
  assert.ok(tar.includes("app/chrome-sandbox"));
  const deb = requiredMembers("linux-deb").map((name) => `./${name}`);
  assert.deepEqual(checkMembers(deb, "linux-deb"), { missing: [], forbidden: [] });
  assert.deepEqual(checkMembers([...deb, "./usr/bin/reasonix-guard"], "linux-deb").forbidden, ["usr/bin/reasonix-guard"]);
  assert.deepEqual(checkMembers(requiredMembers("linux-app-dir").map(String), "linux-app-dir").missing, []);
  assert.throws(() => checkMembers([], "nope"), /unknown artifact kind/);
});

test("macOS service compatibility link stays relative, internal and live", (t) => {
  const root = mkdtempSync(join(tmpdir(), "reasonix-link-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const app = join(root, "Reasonix.app");
  const macOS = join(app, "Contents", "MacOS");
  const service = join(app, "Contents", "Resources", "service");
  mkdirSync(macOS, { recursive: true });
  mkdirSync(service, { recursive: true });
  writeFileSync(join(service, "reasonix-desktop"), "service");
  symlinkSync("../Resources/service/reasonix-desktop", join(macOS, "reasonix-desktop"));
  assert.deepEqual(validateMacServiceLink(app), []);

  rmSync(join(macOS, "reasonix-desktop"));
  symlinkSync("../../../../outside", join(macOS, "reasonix-desktop"));
  assert.match(validateMacServiceLink(app).join("\n"), /does not resolve|dangling/);
});

test("artifact kinds are inferred from release names and bundle shapes", () => {
  assert.equal(inferArtifactKind("/dist/Reasonix-darwin-arm64.zip", false), "darwin-zip");
  assert.equal(inferArtifactKind("/dist/Reasonix-windows-amd64.zip", false), "windows-portable-zip");
  assert.equal(inferArtifactKind("/dist/Reasonix-linux-amd64.tar.gz", false), "linux-tar");
  assert.equal(inferArtifactKind("/dist/Reasonix-linux-amd64.deb", false), "linux-deb");
  assert.equal(inferArtifactKind("/x/Reasonix.app", true, ["Contents"]), "darwin-app-dir");
  assert.equal(inferArtifactKind("/x/app", true, ["Reasonix.exe", "resources"]), "windows-app-dir");
  assert.equal(inferArtifactKind("/x/app", true, ["Reasonix", "chrome-sandbox"]), "linux-app-dir");
  assert.throws(() => inferArtifactKind("/dist/Reasonix-darwin-universal.dmg", false), /cannot infer/);
});

function storedZip(entries, compressed = false) {
  const locals = [];
  const centrals = [];
  let offset = 0;
  for (const [name, content] of entries) {
    const nameBytes = Buffer.from(name, "utf8");
    const data = Buffer.from(content, "utf8");
    const packed = compressed ? deflateRawSync(data) : data;
    const local = Buffer.alloc(30);
    local.writeUInt32LE(0x04034b50, 0);
    local.writeUInt16LE(compressed ? 8 : 0, 8);
    local.writeUInt16LE(nameBytes.length, 26);
    const central = Buffer.alloc(46);
    central.writeUInt32LE(0x02014b50, 0);
    central.writeUInt16LE(compressed ? 8 : 0, 10);
    central.writeUInt32LE(packed.length, 20);
    central.writeUInt32LE(data.length, 24);
    central.writeUInt16LE(nameBytes.length, 28);
    central.writeUInt32LE(offset, 42);
    locals.push(local, nameBytes, packed);
    centrals.push(central, nameBytes);
    offset += local.length + nameBytes.length + packed.length;
  }
  const directory = Buffer.concat(centrals);
  const end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50, 0);
  end.writeUInt16LE(entries.length, 8);
  end.writeUInt16LE(entries.length, 10);
  end.writeUInt32LE(directory.length, 12);
  end.writeUInt32LE(offset, 16);
  return Buffer.concat([...locals, directory, end]);
}

test("zip listing reads the central directory without extracting", () => {
  const dir = mkdtempSync(join(tmpdir(), "reasonix-ziptest-"));
  try {
    const file = join(dir, "Reasonix-darwin-arm64.zip");
    writeFileSync(file, storedZip([["Reasonix.app/", ""], ["Reasonix.app/Contents/MacOS/Reasonix", "mach-o"], ["Reasonix.app/Contents/Resources/app/index.html", "<html>"]]));
    assert.deepEqual(listZipEntries(file), ["Reasonix.app/", "Reasonix.app/Contents/MacOS/Reasonix", "Reasonix.app/Contents/Resources/app/index.html"]);
    writeFileSync(join(dir, "not.zip"), "plain text");
    assert.throws(() => listZipEntries(join(dir, "not.zip")), /not a zip archive/);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("Windows ZIP verification checks launcher bytes and explicitly selects historical layout", t => {
  const dir = mkdtempSync(join(tmpdir(), "reasonix-entry-zip-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const archive = join(dir, "Reasonix-windows-amd64.zip");
  const version = "versions/v1.38.9";
  const entries = [
    ["Reasonix.exe", "gui"], ["reasonix-cli.exe", "cli"], ["current.json", "{}"],
    ...["reasonix-desktop.exe", "reasonix-update-helper.exe", "reasonix-cli.exe", "app/Reasonix.exe", "app/resources/bin/reasonix-cli-launcher.exe", "app/resources/app.asar", "app/resources/app/index.html", "app/resources/build.json"].map(name => [`${version}/${name}`, name]),
  ];
  const verify = mode => spawnSync(process.execPath, [fileURLToPath(new URL("./verify.mjs", import.meta.url)), archive, "--portable-layout", mode], { encoding: "utf8" });
  for (const compressed of [false, true]) {
    writeFileSync(archive, storedZip(entries, compressed));
    assert.equal(readZipMember(archive, "Reasonix.exe").toString(), "gui");
    assert.throws(() => readZipMember(archive, "missing"), /missing/);
    assert.equal(verify("canonical").status, 0);
    assert.notEqual(verify("legacy-dual").status, 0);
    writeFileSync(archive, storedZip([...entries, ["reasonix-launcher.exe", "gui"]], compressed));
    assert.notEqual(verify("canonical").status, 0);
    assert.equal(verify("legacy-dual").status, 0);
    writeFileSync(archive, storedZip([...entries, ["reasonix-launcher.exe", "different"]], compressed));
    assert.notEqual(verify("legacy-dual").status, 0);
  }
  writeFileSync(archive, storedZip([...entries, ["Reasonix.exe", "duplicate"]]));
  assert.throws(() => readZipMember(archive, "Reasonix.exe"), /duplicate/);
});

test("candidate layout declaration agrees with the portable verifier default", () => {
  assert.equal(readFileSync(new URL("./windows-portable-layout.txt", import.meta.url), "utf8").trim(), "canonical");
  assert.ok(!requiredMembers("windows-portable-zip").includes("reasonix-launcher.exe"));
});

test("Linux listings reject a private app directory and unreadable files", () => {
  const deb = parseVerboseListing([
    "drwxr-xr-x root/root         0 2026-09-05 10:00 ./",
    "drwxr-xr-x root/root         0 2026-09-05 10:00 ./usr/lib/reasonix/app/",
    "-rwxr-xr-x root/root 123456789 2026-09-05 10:00 ./usr/lib/reasonix/app/Reasonix",
    "-rwsr-xr-x root/root    123456 2026-09-05 10:00 ./usr/lib/reasonix/app/chrome-sandbox",
    "lrwxrwxrwx root/root         0 2026-09-05 10:00 ./usr/lib/reasonix/app/link -> Reasonix",
  ]);
  assert.deepEqual(deb.map((row) => row.name), ["./", "./usr/lib/reasonix/app/", "./usr/lib/reasonix/app/Reasonix", "./usr/lib/reasonix/app/chrome-sandbox", "./usr/lib/reasonix/app/link"]);
  assert.deepEqual(checkEntryModes(deb, "linux-deb"), []);
  assert.deepEqual(checkMembers(deb.map((row) => row.name), "linux-deb").forbidden, []);

  const privateApp = parseVerboseListing(["drwx------ root/root 0 2026-09-05 10:00 ./usr/lib/reasonix/app/"]);
  assert.deepEqual(checkEntryModes(privateApp, "linux-deb"), ["./usr/lib/reasonix/app/ has mode drwx------; directories must be drwxr-xr-x"]);
  const privateFile = parseVerboseListing(["-rw-r----- root/root 10 2026-09-05 10:00 ./usr/lib/reasonix/app/resources/app.asar"]);
  assert.deepEqual(checkEntryModes(privateFile, "linux-deb"), ["./usr/lib/reasonix/app/resources/app.asar has mode -rw-r-----; files must be world-readable"]);
  const foreignOwner = parseVerboseListing(["-rwxr-xr-x runner/docker 10 2026-09-05 10:00 ./usr/bin/reasonix-desktop"]);
  assert.deepEqual(checkEntryModes(foreignOwner, "linux-deb"), ["./usr/bin/reasonix-desktop is owned by runner/docker; package members must be root/root"]);

  const tar = parseVerboseListing([
    "drwxr-xr-x runner/docker 0 2026-09-05 10:00:00 app/",
    "-rwxr-xr-x runner/docker 42 2026-09-05 10:00:00 reasonix-desktop",
  ]);
  assert.deepEqual(checkEntryModes(tar, "linux-tar"), []);
  assert.throws(() => parseVerboseListing(["drwxr-xr-x  0 runner docker 0 Sep  5 10:00 app/"]), /unrecognised listing line/);
});

test("the packaged app directory is made world-readable before Linux packaging", () => {
  assert.match(read("packaging/package.mjs"), /chmodSync\(bundle, 0o755\)/);
});

test("the Linux package inputs install the Electron tree beside the update helper", () => {
  const nfpm = read("build/linux/nfpm.yaml");
  assert.match(nfpm, /src: \.\/build\/bin\/app\n\s+dst: \/usr\/lib\/reasonix\/app\n\s+type: tree/);
  assert.match(nfpm, /dst: \/usr\/lib\/reasonix\/reasonix-update-helper/);
  assert.match(nfpm, /dst: \/usr\/share\/polkit-1\/actions\/io\.reasonix\.desktop\.update\.policy/);
  assert.match(nfpm, /dst: \/usr\/bin\/reasonix-launcher/);
  assert.doesNotMatch(nfpm, /dst: \/usr\/bin\/reasonix-guard/);
  assert.match(nfpm, /postinstall: \.\/build\/linux\/postinstall\.sh/);
  for (const dep of ["libgtk-3-0", "libnss3", "libgbm1", "libasound2", "pkexec"]) assert.ok(nfpm.includes(`  - ${dep}`), dep);
  const postinstall = read("build/linux/postinstall.sh");
  assert.match(postinstall, /chown root:root \/usr\/lib\/reasonix\/app\/chrome-sandbox/);
  assert.match(postinstall, /chmod 4755 \/usr\/lib\/reasonix\/app\/chrome-sandbox/);
  const entry = read("build/linux/reasonix.desktop");
  assert.match(entry, /^Exec=reasonix-launcher$/m);
  assert.match(entry, /^Icon=reasonix-desktop$/m);
  assert.match(entry, /^StartupWMClass=Reasonix$/m);
});

test("the NSIS script installs the Electron tree with both payload modes and no WebView2", () => {
  const nsi = read("build/windows/installer/project.nsi");
  assert.ok(nsi.startsWith("﻿"), "UTF-8 BOM");
  assert.match(nsi, /!include "reasonix_project\.nsh"/);
  assert.doesNotMatch(nsi, /wails_tools\.nsh/);
  assert.doesNotMatch(nsi, /webview2/i);
  assert.equal((nsi.match(/!insertmacro reasonix\.files/g) ?? []).length, 2, "stage payload and normal install both extract the payload");
  assert.match(nsi, /File \/r "app"/);
  assert.match(nsi, /ARG_REASONIX_AMD64_BINARY/);
  assert.match(nsi, /ARG_REASONIX_ARM64_BINARY/);
  assert.match(nsi, /!define UNINST_KEY_NAME "\$\{INFO_COMPANYNAME\}\$\{INFO_PRODUCTNAME\}"/);
  assert.match(nsi, /!define PRODUCT_EXECUTABLE "\$\{INFO_PROJECTNAME\}\.exe"/);
  assert.match(nsi, /RMDir \/r "\$INSTDIR\\versions"/);
  assert.match(nsi, /File "\/oname=uninstall\.exe" "\$\{ARG_REASONIX_SIGNED_UNINSTALLER\}"/);
  for (const releaseIdentity of [
    /\$INSTDIR\\versions\\\$\{REASONIX_VERSION_TAG\}/,
    /\.installer-\$\{REASONIX_VERSION_TAG\}-\$R8/,
    /--version "\$\{REASONIX_VERSION_TAG\}"/,
  ]) assert.match(nsi, releaseIdentity);
  for (const nativeIdentityLeak of [
    /\$INSTDIR\\versions\\v\$\{INFO_PRODUCTVERSION\}/,
    /\.installer-v\$\{INFO_PRODUCTVERSION\}/,
    /--version "v\$\{INFO_PRODUCTVERSION\}"/,
  ]) assert.doesNotMatch(nsi, nativeIdentityLeak);
  assert.deepEqual(
    nsi.split(/\r?\n/).map(line => line.trim()).filter(line => line.includes("INFO_PRODUCTVERSION")),
    [
      "## INFO_PRODUCTVERSION is numeric metadata only.",
      'VIProductVersion "${INFO_PRODUCTVERSION}.0"',
      'VIFileVersion    "${INFO_PRODUCTVERSION}.0"',
    ],
    "numeric resource versions must never become install or runtime identity",
  );
  const activation = nsi.slice(nsi.indexOf("Reasonix layout activator output:"));
  const retry = activation.indexOf('MessageBox MB_ICONEXCLAMATION|MB_RETRYCANCEL "$(reasonixActivateLocked)" IDRETRY reasonix_layout_activate');
  assert.ok(retry > 0, "activation failure offers Retry against the kept staging directory");
  assert.ok(activation.indexOf('RMDir /r "$R9"') > retry, "staging is discarded only after the user gives up");
});

test("the installer stamps the shortcuts it created without launching the desktop", () => {
  const nsi = read("build/windows/installer/project.nsi");
  const maintenance = nsi.indexOf('--repair-shortcuts "$SMPROGRAMS\\${INFO_PRODUCTNAME}.lnk" "$DESKTOP\\${INFO_PRODUCTNAME}.lnk"');
  assert.ok(maintenance > nsi.indexOf('CreateShortCut "$DESKTOP\\${INFO_PRODUCTNAME}.lnk"'), "maintenance follows shortcut creation");
  assert.match(nsi.slice(maintenance, maintenance + 350), /Pop \$0/);
  assert.match(nsi.slice(maintenance, maintenance + 350), /shortcut identity repair failed/);
});

test("installer unlock checks do not create or lock missing release entries", () => {
  const nsi = read("build/windows/installer/project.nsi");
  const body = nsi.slice(nsi.indexOf("Function reasonix.waitForExecutableUnlock"), nsi.indexOf("FunctionEnd", nsi.indexOf("Function reasonix.waitForExecutableUnlock")));
  const opens = [...body.matchAll(/FileOpen \$1 "([^"]+)" a/g)];
  assert.equal(opens.length, 6);
  for (const open of opens) {
    const preceding = body.slice(0, open.index);
    const guard = `IfFileExists "${open[1]}" 0 `;
    const at = preceding.lastIndexOf(guard);
    assert.ok(at >= 0, `missing existence guard for ${open[1]}`);
    assert.match(preceding.slice(at), /^IfFileExists [^\n]+\r?\n\s+ClearErrors\s+$/);
  }
});

function elf64(interpreter) {
  const phoff = 64, phentsize = 56, phnum = interpreter ? 2 : 1;
  const dataOffset = phoff + phnum * phentsize;
  const path = Buffer.from(interpreter ? `${interpreter}\0` : "", "latin1");
  const bytes = Buffer.alloc(dataOffset + path.length);
  bytes.writeUInt32BE(0x7f454c46, 0);
  bytes[4] = 2;
  bytes[5] = 1;
  bytes[6] = 1;
  bytes.writeUInt16LE(2, 16);
  bytes.writeUInt16LE(62, 18);
  bytes.writeBigUInt64LE(BigInt(phoff), 32);
  bytes.writeUInt16LE(64, 52);
  bytes.writeUInt16LE(phentsize, 54);
  bytes.writeUInt16LE(phnum, 56);
  bytes.writeUInt32LE(1, phoff);
  if (interpreter) {
    const header = phoff + phentsize;
    bytes.writeUInt32LE(3, header);
    bytes.writeBigUInt64LE(BigInt(dataOffset), header + 8);
    bytes.writeBigUInt64LE(BigInt(path.length), header + 32);
    path.copy(bytes, dataOffset);
  }
  return bytes;
}

test("ELF program headers say whether a binary needs the host's dynamic loader", () => {
  assert.equal(elfInterpreter(elf64("/lib64/ld-linux-x86-64.so.2")), "/lib64/ld-linux-x86-64.so.2");
  assert.equal(elfInterpreter(elf64(null)), null);
  assert.throws(() => elfInterpreter(Buffer.from("#!/bin/sh\n")), /not an ELF/);
  assert.throws(() => elfInterpreter(elf64("/lib/ld.so").subarray(0, 100)), /truncated/);
});

test("Linux archives refuse a Go binary linked against the build host's glibc", () => {
  const dynamic = new Set(["reasonix-desktop", "usr/bin/reasonix-desktop"]);
  const read = (name) => elf64(dynamic.has(name) ? "/lib64/ld-linux-x86-64.so.2" : null);
  for (const kind of ["linux-tar", "linux-deb"]) {
    const errors = checkStaticGoMembers(kind, read);
    assert.equal(errors.length, 1, `${kind}: ${errors.join("; ")}`);
    assert.match(errors[0], /reasonix-desktop is dynamically linked/);
  }
  const seen = [];
  assert.deepEqual(checkStaticGoMembers("linux-deb", (name) => (seen.push(name), elf64(null))), []);
  assert.ok(seen.includes("usr/lib/reasonix/reasonix-update-helper"));
  assert.deepEqual(checkStaticGoMembers("linux-app-dir", () => assert.fail("app dir has no Go members")), []);
});
