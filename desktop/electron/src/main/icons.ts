import { existsSync } from "node:fs";
import { join, resolve } from "node:path";

export interface IconLookup {
  platform: NodeJS.Platform;
  appPath: string;
  resourcesPath: string;
  packaged: boolean;
}

export interface IconCandidates {
  tray: string[];
  window: string[];
  dock: string[];
}

export function iconCandidates(input: IconLookup): IconCandidates {
  const build = input.packaged ? join(input.resourcesPath, "icons") : resolve(input.appPath, "..", "build");
  const hicolor = (size: string) => join(build, "linux", "icons", "hicolor", size, "apps", "reasonix-desktop.png");
  const appicon = join(build, "appicon.png");
  const trayTemplate = join(build, "trayTemplate.png");
  return {
    // macOS must not fall through to the full-canvas color appicon for the
    // tray: template-izing it renders the alpha mask as a solid block (#10715).
    tray: input.platform === "darwin" ? [trayTemplate, hicolor("32x32"), appicon] : [hicolor("32x32"), appicon],
    window: [hicolor("256x256"), appicon],
    // Packaged macOS apps keep their bundle's ICNS. Development uses the same
    // inset artwork instead of the full-canvas Windows/Linux window icon.
    dock: input.platform === "darwin" && !input.packaged ? [join(build, "darwin", "appicon.png")] : [],
  };
}

export function firstExisting(paths: string[]): string | null {
  return paths.find((path) => existsSync(path)) ?? null;
}
