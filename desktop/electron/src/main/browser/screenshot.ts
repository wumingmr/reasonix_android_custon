import { existsSync, statSync } from "node:fs";
import { writeFile, rename, rm } from "node:fs/promises";
import { isAbsolute, join } from "node:path";
import type { DocumentBinding } from "./documents.js";
import type { GuestPage } from "./guestView.js";
import { resolveRef } from "./refResolver.js";
import { browserFailure, staleReference } from "./errors.js";
import { MAX_CAPTURE_PIXELS, validatePNG } from "./png.js";
import { acquireDebugger } from "./debuggerLease.js";
export { pngSize } from "./png.js";

export interface ScreenshotRequest {
  ref: string;
  fullPage: boolean;
  directory: string;
}

export interface ScreenshotResult {
  path: string;
  mime: "image/png";
  width: number;
  height: number;
  reason?: string;
  observationToken?: string;
  cssWidth?: number;
  cssHeight?: number;
}

export interface ScreenshotDeps {
  directoryExists?(path: string): boolean;
  writeFile?(path: string, data: Buffer): void | Promise<void>;
  decodePNG?(data: Buffer): { width: number; height: number };
  verify?(): void;
  viewport?: { width: number; height: number } | null;
  pixelRatio?: number;
  now?(): number;
}

let sequence = 0;

const defaultDirectoryExists = (path: string) => {
  try {
    return existsSync(path) && statSync(path).isDirectory();
  } catch {
    return false;
  }
};

export function screenshotPath(directory: string, now: number): string {
  sequence += 1;
  return join(directory, `shot-${now}-${sequence}.png`);
}

// Full-page captures go through the DevTools protocol because a
// WebContentsView cannot be resized past the window; everything else uses
// capturePage on the visible viewport or the element's rect.
export async function captureScreenshot(page: GuestPage, binding: DocumentBinding | null, zoom: number, request: ScreenshotRequest, deps: ScreenshotDeps = {}): Promise<ScreenshotResult> {
  const directoryExists = deps.directoryExists ?? defaultDirectoryExists;
  if (!isAbsolute(request.directory) || !directoryExists(request.directory)) throw new Error("screenshot directory must be an existing absolute path");
  const target = screenshotPath(request.directory, (deps.now ?? Date.now)());
  deps.verify?.();
  let png: Buffer;
  let size: { width: number; height: number };
  if (request.fullPage) {
    const full = await captureFullPage(page, deps.pixelRatio ?? 1);
    png = full.png;
    size = full.size;
  } else {
    let rect: Electron.Rectangle | undefined;
    if (request.ref !== "") {
      if (!binding) throw new Error("element screenshots need a snapshot first");
      const resolved = await resolveRef(page, binding, request.ref, true);
      if (resolved.ok) {
        const { element } = resolved.value;
        rect = {
          x: Math.max(0, Math.floor(element.x * zoom)),
          y: Math.max(0, Math.floor(element.y * zoom)),
          width: Math.max(1, Math.ceil(element.width * zoom)),
          height: Math.max(1, Math.ceil(element.height * zoom)),
        };
      } else throw staleReference(`cannot capture the requested element: ${resolved.reason}`);
    }
    if (deps.viewport) {
      const clip = rect ? { x: rect.x / zoom, y: rect.y / zoom, width: rect.width / zoom, height: rect.height / zoom } : { x: 0, y: 0, ...deps.viewport };
      const dbg = page.debugger;
      const release = acquireDebugger(dbg);
      try {
        const metrics = await release.send("Page.getLayoutMetrics") as { cssVisualViewport?: { pageX: number; pageY: number } };
        const result = await release.send("Page.captureScreenshot", { format: "png", fromSurface: true, captureBeyondViewport: true, clip: { ...clip, x: clip.x + (metrics.cssVisualViewport?.pageX ?? 0), y: clip.y + (metrics.cssVisualViewport?.pageY ?? 0), scale: 1 / (deps.pixelRatio ?? 1) } }) as { data?: string };
        png = Buffer.from(result.data ?? "", "base64");
        size = { width: Math.round(clip.width), height: Math.round(clip.height) };
      } finally { release(); }
    } else {
      const image = await page.capturePage(rect);
      png = image.toPNG();
      size = image.getSize();
    }
  }
  const actual = validatePNG(png);
  const decoded = deps.decodePNG?.(png) ?? actual;
  if (actual.width !== size.width || actual.height !== size.height || decoded.width !== actual.width || decoded.height !== actual.height) {
    throw browserFailure("invalid_image", `PNG dimensions ${actual.width}x${actual.height} do not match captured surface ${size.width}x${size.height} (decoded ${decoded.width}x${decoded.height})`);
  }
  deps.verify?.();
  if (deps.writeFile) await deps.writeFile(target, png);
  else {
    const temporary = `${target}.tmp`;
    try {
      await writeFile(temporary, png, { flag: "wx", mode: 0o600 });
      deps.verify?.();
      await rename(temporary, target);
      deps.verify?.();
    } catch (error) {
      await rm(temporary, { force: true });
      await rm(target, { force: true });
      throw error;
    }
  }
  return { path: target, mime: "image/png", width: actual.width, height: actual.height };
}

async function captureFullPage(page: GuestPage, pixelRatio: number): Promise<{ png: Buffer; size: { width: number; height: number } }> {
  const dbg = page.debugger;
  const release = acquireDebugger(dbg);
  try {
    const metrics = await release.send("Page.getLayoutMetrics") as { cssContentSize?: { width: number; height: number } };
    const content = metrics.cssContentSize;
    if (!content || !Number.isFinite(content.width * content.height) || content.width <= 0 || content.height <= 0 || content.width * content.height > MAX_CAPTURE_PIXELS) {
      throw browserFailure("invalid_image", "full page exceeds the capture pixel budget; capture the viewport or an element");
    }
    const size = { width: Math.ceil(content.width), height: Math.ceil(content.height) };
    if (size.width * size.height > MAX_CAPTURE_PIXELS) throw browserFailure("invalid_image", "full page exceeds the capture pixel budget");
    const result = (await release.send("Page.captureScreenshot", { format: "png", captureBeyondViewport: true, fromSurface: true, clip: { x: 0, y: 0, ...size, scale: 1 / pixelRatio } })) as { data?: unknown };
    if (typeof result?.data !== "string") throw new Error("Page.captureScreenshot returned no image");
    return { png: Buffer.from(result.data, "base64"), size };
  } finally {
    release();
  }
}
