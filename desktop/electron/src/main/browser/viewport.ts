export interface BrowserViewport { width: number; height: number; scale: "fit" | number }
export function validateViewport(input: BrowserViewport): BrowserViewport {
  if (!Number.isInteger(input.width) || input.width < 320 || input.width > 3840 || !Number.isInteger(input.height) || input.height < 320 || input.height > 2160) throw new Error("viewport must be 320–3840 × 320–2160 CSS pixels");
  if (input.scale !== "fit" && ![0.5, 0.75, 1, 1.25, 1.5, 2].includes(input.scale)) throw new Error("unsupported viewport display scale");
  return { ...input };
}
export function viewportScale(viewport: BrowserViewport | null, surface: { width: number; height: number }): number {
  if (!viewport) return 1;
  return viewport.scale === "fit" ? Math.max(0.05, Math.min(surface.width / viewport.width, surface.height / viewport.height, 2)) : viewport.scale;
}
