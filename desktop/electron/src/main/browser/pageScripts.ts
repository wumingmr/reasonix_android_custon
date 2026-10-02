import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

export type { RefInput, ResolveInput, ResolvedElement, ResolveFailure, ResolveOutput, SelectInput, SelectOutput, LocateOutput, IdentityInput } from "./pageActions.js";

export const SNAPSHOT_SCRIPT_SOURCE = "pageSnapshot";
export const RESOLVE_SCRIPT_SOURCE = "pageResolve";
export const SELECT_SCRIPT_SOURCE = "pageSelect";
export const FOCUS_SCRIPT_SOURCE = "pageFocus";
export const IDENTITY_SCRIPT_SOURCE = "pageIdentity";
export const LOCATE_SCRIPT_SOURCE = "pageLocate";

let cachedSource: string | undefined;

export function pageRuntimeSource(): string {
  if (cachedSource !== undefined) return cachedSource;
  const directory = typeof __dirname === "string" ? __dirname : resolve(dirname(fileURLToPath(import.meta.url)), "../../../dist");
  const source = readFileSync(resolve(directory, "browser-page.js"), "utf8");
  const manifest = JSON.parse(readFileSync(resolve(directory, "browser-page.json"), "utf8")) as { version?: number; sha256?: string };
  if (manifest.version !== 1 || manifest.sha256 !== createHash("sha256").update(source).digest("hex")) {
    throw new Error("browser page runtime integrity check failed");
  }
  cachedSource = source;
  return source;
}

export function scriptCall(method: string, input: unknown): string {
  if (!/^page[A-Za-z]+$/.test(method)) throw new Error("invalid browser page method");
  return `/* reasonix:${method} */ (() => {\n${pageRuntimeSource()}\nreturn ReasonixPageRuntime[${JSON.stringify(method)}](${JSON.stringify(input)});\n})()`;
}
