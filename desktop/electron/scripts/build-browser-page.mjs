import { build } from "esbuild";
import { createHash } from "node:crypto";
import { mkdir, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { playwrightPageResource } from "./playwright-page-resource.mjs";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");

export async function buildBrowserPage() {
  await mkdir(resolve(root, "dist"), { recursive: true });
  const playwright = await playwrightPageResource(resolve(root, "dist"));
  const result = await build({
    entryPoints: [resolve(root, "src/main/browser/pageRuntimeEntry.ts")],
    bundle: true, platform: "browser", format: "iife", globalName: "ReasonixPageRuntime",
    target: "es2022", keepNames: false, minify: true, write: false,
    plugins: [{ name: "qualified-playwright-injection", setup(build) {
      build.onResolve({ filter: /^reasonix-playwright-injected$/ }, () => ({ path: "injected", namespace: "reasonix-injected" }));
      build.onLoad({ filter: /.*/, namespace: "reasonix-injected" }, () => ({ contents: playwright.source, loader: "js" }));
    } }],
  });
  const source = result.outputFiles[0].text;
  await mkdir(resolve(root, "dist"), { recursive: true });
  await writeFile(resolve(root, "dist/browser-page.js"), source);
  await writeFile(resolve(root, "dist/browser-page.json"), JSON.stringify({ version: 1, playwright: playwright.version, adapter: "password-values-redacted-v1", sha256: createHash("sha256").update(source).digest("hex") }) + "\n");
  await build({ entryPoints: [resolve(root, "src/main/browser/recorderRenderer.ts")], outfile: resolve(root, "dist/browser-recorder.js"), bundle: true, platform: "browser", format: "iife", target: "es2022", keepNames: false, minify: true });
  await build({ entryPoints: [resolve(root, "src/main/browser/recorderPreload.ts")], outfile: resolve(root, "dist/recorder-preload.cjs"), bundle: true, platform: "node", format: "cjs", external: ["electron"], target: "node22", minify: true });
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await buildBrowserPage();
