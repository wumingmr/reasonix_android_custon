import { existsSync } from "node:fs";
import { basename } from "node:path";
import { Menu, nativeImage, Tray } from "electron";
import type { TrayLabels } from "./hostCalls.js";
import { errorText, type Logger } from "./log.js";

export interface TrayDeps {
  platform: NodeJS.Platform;
  iconPath: string | null;
  onOpen(): void;
  onQuit(): void;
  log: Logger;
}

// macOS template-icon naming convention (`nameTemplate.png` / `nameTemplate@2x.png`).
const TEMPLATE_ICON_FILE = /Template(@2x)?\.png$/;

function isTemplateIconPath(path: string): boolean {
  return TEMPLATE_ICON_FILE.test(basename(path));
}

// Loads a 1x template PNG and attaches its @2x sibling as a retina
// representation so the menu bar glyph stays sharp on scaled displays.
function templateTrayImage(path: string): Electron.NativeImage | null {
  const image = nativeImage.createFromPath(path);
  if (image.isEmpty()) return null;
  const retinaPath = path.replace(/\.png$/, "@2x.png");
  if (existsSync(retinaPath)) {
    const retina = nativeImage.createFromPath(retinaPath);
    if (!retina.isEmpty()) image.addRepresentation({ scaleFactor: 2, buffer: retina.toPNG() });
  }
  return image;
}

export class TrayHost {
  private tray: Tray | null = null;

  constructor(private readonly deps: TrayDeps) {}

  ensure(labels: TrayLabels): { ready: boolean; reason: string } {
    const { deps } = this;
    if (!deps.iconPath) return { ready: false, reason: "tray icon asset missing" };
    try {
      if (!this.tray) {
        let image = nativeImage.createFromPath(deps.iconPath);
        if (image.isEmpty()) return { ready: false, reason: `tray icon could not be decoded: ${deps.iconPath}` };
        if (deps.platform === "darwin") {
          const template = isTemplateIconPath(deps.iconPath) ? templateTrayImage(deps.iconPath) : null;
          if (template) {
            template.setTemplateImage(true);
            image = template;
          } else {
            // Full-canvas color artwork must never be template-ized: the alpha
            // mask renders as a solid black block (#10715). A small color icon
            // is the least-broken fallback when the template asset is missing.
            image = image.resize({ width: 18, height: 18 });
          }
        } else if (deps.platform === "win32") {
          image = image.resize({ width: 16, height: 16 });
        }
        const tray = new Tray(image);
        tray.on("click", () => deps.onOpen());
        this.tray = tray;
      }
      this.tray.setToolTip(labels.tooltip || "Reasonix");
      this.tray.setContextMenu(Menu.buildFromTemplate([
        { label: labels.openTitle, toolTip: labels.openTooltip, click: () => deps.onOpen() },
        { label: labels.quitTitle, toolTip: labels.quitTooltip, click: () => deps.onQuit() },
      ]));
      return { ready: true, reason: "" };
    } catch (error) {
      deps.log.warn(`tray unavailable: ${errorText(error)}`);
      this.destroy();
      return { ready: false, reason: errorText(error) };
    }
  }

  destroy(): void {
    try {
      this.tray?.destroy();
    } catch {
      // Already gone.
    }
    this.tray = null;
  }
}
