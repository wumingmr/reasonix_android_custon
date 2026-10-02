// Cross-platform path identity used by chat file recognition. The flavor rules
// here are what keep a Windows drive path and a POSIX path with a literal
// backslash from collapsing into each other.
import assert from "node:assert/strict";
import {
  declaredPathFlavor, fileIdentity, fileURLToPath, isAbsolutePath, isUncPath,
  isWindowsDrivePath, pathBasename, pathExtension,
} from "../lib/filePaths";

// ── Windows drive, UNC, and separators ──────────────────────────────────────
assert.equal(fileIdentity("C:\\Users\\me\\out.svg"), "C:/Users/me/out.svg");
assert.equal(fileIdentity("c:/users/me/out.svg"), "C:/users/me/out.svg", "drive letter folds case");
assert.equal(fileIdentity("C:\\Users\\me\\out.svg"), fileIdentity("c:/Users/me/out.svg"));
assert.equal(fileIdentity("C:\\a\\..\\b\\c.go"), "C:/b/c.go");
assert.equal(fileIdentity("C:\\..\\..\\b.go"), "C:/b.go", "a drive root is never folded away");
assert.equal(fileIdentity("C:\\.."), "C:/");
assert.equal(fileIdentity("\\\\server\\share\\dir\\a.txt"), "//server/share/dir/a.txt");
assert.equal(fileIdentity("//server/share/dir/a.txt"), "//server/share/dir/a.txt");
assert.equal(fileIdentity("\\\\server\\share\\a\\..\\b.txt"), "//server/share/b.txt");
assert.equal(fileIdentity("\\\\server\\share\\..\\..\\b.txt"), "//server/share/b.txt", "a UNC share root is preserved");
assert(isUncPath("\\\\server\\share"), "UNC detection");
assert(isUncPath("//server/share"), "UNC forward-slash detection");
assert(isWindowsDrivePath("C:/x"), "forward-slash drive path detection");
assert.equal(declaredPathFlavor("C:\\x"), "windows");
assert.equal(declaredPathFlavor("/home/x"), "posix");
assert.equal(declaredPathFlavor("src/x.ts"), undefined, "relative paths declare no flavor");

// ── POSIX, including the backslash rule ─────────────────────────────────────
assert.equal(fileIdentity("/home/me/out.svg"), "/home/me/out.svg");
assert.equal(fileIdentity("/home/me/./sub/../out.svg"), "/home/me/out.svg");
assert.equal(fileIdentity("/../../etc/passwd"), "/etc/passwd", ".. never escapes the POSIX root");
assert.equal(fileIdentity("/tmp/a\\b.svg"), "/tmp/a\\b.svg", "a POSIX path keeps literal backslashes");
assert.notEqual(fileIdentity("/tmp/a\\b.svg"), fileIdentity("/tmp/a/b.svg"));
assert.equal(fileIdentity("out/a.svg", "posix"), "out/a.svg");
assert.equal(fileIdentity("out\\a.svg", "posix"), "out\\a.svg", "a POSIX host keeps the backslash");
assert.equal(fileIdentity("out\\a.svg", "windows"), "out/a.svg");
assert.equal(fileIdentity("out\\a.svg"), "out\\a.svg", "an ambiguous relative path preserves a literal backslash");
assert.notEqual(fileIdentity("out\\a.svg"), fileIdentity("out/a.svg"), "the renderer never merges host-ambiguous paths");

// ── file:// URLs decode once, ordinary paths never decode ───────────────────
assert.equal(fileURLToPath("file:///home/me/a%20b.svg"), "/home/me/a b.svg");
assert.equal(fileURLToPath("file:///C:/Users/me/a.svg"), "C:/Users/me/a.svg");
assert.equal(fileURLToPath("file://localhost/home/me/a.svg"), "/home/me/a.svg");
assert.equal(fileURLToPath("file://server/share/a.svg"), "//server/share/a.svg", "an authority form is a UNC file URL");
assert.equal(fileURLToPath("file:///home/me/a.svg?v=1"), undefined);
assert.equal(fileURLToPath("file:////./PhysicalDrive0"), undefined, "a device authority is never a file path");
assert.equal(fileURLToPath("FILE:///home/me/a.svg"), undefined, "the scheme match is case-sensitive like the link allowlist");
assert.equal(fileIdentity("file:///home/me/a%20b.svg"), "/home/me/a b.svg");
assert.equal(fileIdentity("file://server/share/a.svg"), "//server/share/a.svg", "a UNC file URL keeps its share root");
assert.equal(fileIdentity("/home/me/a%20b.svg"), "/home/me/a%20b.svg", "a literal %20 is not an escape");
assert.notEqual(fileIdentity("/home/me/a%20b.svg"), fileIdentity("/home/me/a b.svg"));
assert.equal(fileIdentity("  /tmp/x.svg  "), "/tmp/x.svg", "surrounding whitespace is trimmed");

// ── Space, CJK, parentheses, and other legal filename bytes survive ─────────
assert.equal(fileIdentity("/Users/me/我的 项目/图 (1).svg"), "/Users/me/我的 项目/图 (1).svg");
assert.equal(fileIdentity("C:\\我的 项目\\图 (1).svg"), "C:/我的 项目/图 (1).svg");
assert.equal(pathBasename("/Users/me/我的 项目/图 (1).svg"), "图 (1).svg");

// ── Case is preserved, so a case-sensitive volume is never merged ───────────
assert.notEqual(fileIdentity("/Users/me/Out.svg"), fileIdentity("/Users/me/out.svg"));
assert.notEqual(fileIdentity("/home/me/Readme.md"), fileIdentity("/home/me/readme.md"));

// ── Basename, extension, and absoluteness ───────────────────────────────────
assert.equal(pathBasename("C:\\a\\b\\c.tar.gz"), "c.tar.gz");
assert.equal(pathBasename("/a/b/"), "b");
assert.equal(pathBasename("file:///a/b/c.svg"), "c.svg");
assert.equal(pathExtension("a/b/c.TAR.GZ"), "gz");
assert.equal(pathExtension("/a/.gitignore"), "", "a dotfile has no extension");
assert.equal(pathExtension("a/b."), "");
assert(isAbsolutePath("/home/x"));
assert(isAbsolutePath("C:\\x"));
assert(isAbsolutePath("\\\\server\\share\\x"));
assert(isAbsolutePath("file:///home/x"));
assert(!isAbsolutePath("src/x.ts"));
assert(!isAbsolutePath("src\\x.ts"), "a relative path is not absolute on either flavor");

// ── Same-file aliases and same-name ambiguity ───────────────────────────────
assert.equal(
  fileIdentity("./out/diagram.svg"),
  fileIdentity("out/diagram.svg"),
  "a ./ prefix names the same relative file",
);
assert.equal(
  fileIdentity("C:\\repo\\out\\diagram.svg"),
  fileIdentity("c:/repo/./out/../out/diagram.svg"),
);
assert.equal(
  fileIdentity("/repo/out/diagram.svg"),
  fileIdentity("/repo/out/../out/diagram.svg"),
);
assert.notEqual(fileIdentity("out/diagram.svg"), fileIdentity("/repo/out/diagram.svg"), "a relative and an absolute spelling stay distinct until the host resolves them");
assert.equal(pathBasename("/a/diagram.svg"), pathBasename("/b/diagram.svg"), "two files can share a basename");

console.log("file paths: three-platform identity and URL decoding passed");
