// filePaths — the one cross-platform path parser shared by chat file
// recognition, the presented-file link map, and the file resource menu.
//
// This module owns the platform-independent half of path identity: which text
// is a path, which separator a flavor uses, and when two spellings name the
// same file. It never decides whether a file exists or may be opened — the
// desktop host resolves every candidate on the source host, where the real
// filesystem (and its case rules) lives. Keeping this layer conservative
// matters: over-merging two spellings only costs a display group, while a
// wrong merge would point a click at the wrong file.

import { localPathFromHref } from "./localFileUrl";

/** Separator/root conventions of the filesystem that owns a path. */
export type PathFlavor = "windows" | "posix";

const WINDOWS_DRIVE_RE = /^[A-Za-z]:[\\/]/;

export function isWindowsDrivePath(path: string): boolean {
  return WINDOWS_DRIVE_RE.test(path);
}

/** A drive root still counts when it carries no trailing separator (`C:`). */
export function isWindowsDriveRoot(path: string): boolean {
  return path.length === 2 && /^[A-Za-z]:$/.test(path);
}

/** `\\server\share\...` and its forward-slash twin `//server/share/...`. */
export function isUncPath(path: string): boolean {
  return /^[\\/]{2}[^\\/]/.test(path);
}

/**
 * The flavor a path declares by its own shape. Relative paths return
 * `undefined` because their separator rule belongs to the session's host, not
 * to the text — `src\a.svg` is two segments on Windows and one odd filename on
 * Linux. Callers that know the host pass its flavor explicitly.
 */
export function declaredPathFlavor(path: string): PathFlavor | undefined {
  if (isWindowsDrivePath(path) || isUncPath(path)) return "windows";
  if (path.startsWith("/")) return "posix";
  return undefined;
}

/**
 * The local path a `file://` URL names, decoded exactly once, or `undefined`
 * for anything else. It delegates to the canonical Markdown link parser so this
 * module cannot drift from the allowlist that already rejects a query, a
 * fragment, a device authority, and an alternate data stream. An ordinary path
 * is never decoded: a literal `%20` in a filename is not an escape.
 */
export function fileURLToPath(raw: string): string | undefined {
  return localPathFromHref(raw.trim()) ?? undefined;
}

/** Separator set for a flavor; POSIX filenames may legally contain `\`. */
function separatorsFor(flavor: PathFlavor): string[] {
  return flavor === "windows" ? ["/", "\\"] : ["/"];
}

function splitSegments(path: string, flavor: PathFlavor): string[] {
  const separators = separatorsFor(flavor);
  const parts: string[] = [];
  let current = "";
  for (const character of path) {
    if (separators.includes(character)) {
      parts.push(current);
      current = "";
      continue;
    }
    current += character;
  }
  parts.push(current);
  return parts;
}

/**
 * Segment count that `..` may never fold away: a UNC share root is two
 * segments, a drive letter is one, and a POSIX root is none.
 */
function rootFloor(resolved: PathFlavor, unc: boolean, drive: boolean): number {
  if (unc) return 2;
  if (resolved === "windows" && drive) return 1;
  return 0;
}

/**
 * Canonical grouping key for a path: separators unified, `.`/`..` folded, and
 * the UNC or drive root preserved. Case is deliberately preserved — only the
 * source host's filesystem may decide whether two casings are one file.
 */
export function fileIdentity(path: string, flavor?: PathFlavor): string {
  const raw = path.trim();
  if (!raw) return "";
  const fromURL = fileURLToPath(raw);
  // A malformed `file:` URL has no identity; it must not fall back to being
  // treated as an ordinary path.
  if (fromURL === undefined && /^file:/i.test(raw)) return "";
  const value = fromURL ?? raw;
  // Keep an ambiguous relative path literal until the source host supplies its
  // separator semantics, matching Harness' host-owned file identity boundary.
  const resolved = flavor ?? declaredPathFlavor(value) ?? "posix";
  const unc = resolved === "windows" && isUncPath(value);
  const absolute = unc || (resolved === "windows" ? isWindowsDrivePath(value) : value.startsWith("/"));

  const segments: string[] = [];
  const floor = rootFloor(resolved, unc, absolute);
  for (const part of splitSegments(value, resolved)) {
    if (!part || part === ".") continue;
    if (part === "..") {
      if (absolute && segments.length <= floor) continue;
      if (!absolute && segments.length === 0) { segments.push(part); continue; }
      if (segments[segments.length - 1] === "..") { segments.push(part); continue; }
      segments.pop();
      continue;
    }
    segments.push(part);
  }

  // A drive letter is case-insensitive at the OS level, unlike the rest of the
  // path, so it is the one segment that may be folded. It also replaces the
  // leading separator: the identity of `C:\a` is `C:/a`, not `/C:/a`.
  const drive = resolved === "windows" && segments.length > 0 && /^[A-Za-z]:$/.test(segments[0]);
  if (drive) segments[0] = segments[0].toUpperCase();
  const identity = segments.join("/");
  if (unc) return `//${identity}`;
  // `C:\` and `C:\..` are the drive root; a bare `C:` is drive-relative and
  // stays distinct from it.
  if (drive) return absolute && segments.length === 1 ? `${segments[0]}/` : identity;
  return absolute ? `/${identity}` : identity;
}

export function pathBasename(path: string, flavor?: PathFlavor): string {
  const value = fileURLToPath(path) ?? path.trim();
  if (!value) return "";
  const resolved = flavor ?? declaredPathFlavor(value) ?? "posix";
  const parts = splitSegments(value, resolved).filter(Boolean);
  return parts[parts.length - 1] ?? "";
}

export function pathExtension(path: string): string {
  const name = pathBasename(path);
  const dot = name.lastIndexOf(".");
  if (dot <= 0 || dot === name.length - 1) return "";
  return name.slice(dot + 1).toLowerCase();
}

export function isAbsolutePath(path: string, flavor?: PathFlavor): boolean {
  const value = fileURLToPath(path) ?? path.trim();
  if (!value) return false;
  if (isWindowsDrivePath(value) || isWindowsDriveRoot(value) || isUncPath(value)) return true;
  const resolved = flavor ?? declaredPathFlavor(value);
  if (resolved === "windows") return isWindowsDrivePath(value);
  return value.startsWith("/");
}
