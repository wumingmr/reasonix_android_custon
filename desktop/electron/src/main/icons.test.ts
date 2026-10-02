import assert from "node:assert/strict";
import { dirname, join, resolve } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { firstExisting, iconCandidates } from "./icons.js";

const appPath = resolve(dirname(fileURLToPath(import.meta.url)), "../..");

test("development Dock uses the macOS artwork and never a full-canvas fallback", () => {
  const icons = iconCandidates({ platform: "darwin", appPath, resourcesPath: "/unused", packaged: false });
  const macIcon = resolve(appPath, "../build/darwin/appicon.png");
  assert.deepEqual(icons.dock, [macIcon]);
  assert.equal(firstExisting(icons.dock), macIcon);
  assert.ok(!icons.window.includes(macIcon));
});

test("packaged macOS preserves the bundle icon and other platforms do not set a Dock icon", () => {
  for (const platform of ["darwin", "win32", "linux"] as const) {
    for (const packaged of [false, true]) {
      if (platform === "darwin" && !packaged) continue;
      const icons = iconCandidates({ platform, appPath, resourcesPath: "/unused", packaged });
      assert.equal(firstExisting(icons.dock), null, `${platform}, packaged=${packaged}`);
    }
  }
});

test("macOS tray resolves the dedicated template asset and never the full-canvas appicon", () => {
  // The color artwork stays in the candidate list as a last-resort fallback
  // (TrayHost renders it as a small color icon, never template-izes it); what
  // must hold is that resolution lands on the template while it exists.
  const dev = iconCandidates({ platform: "darwin", appPath, resourcesPath: "/unused", packaged: false });
  assert.ok(dev.tray[0].endsWith("trayTemplate.png"));
  assert.equal(firstExisting(dev.tray), resolve(appPath, "../build/trayTemplate.png"));
  const packagedIcons = iconCandidates({ platform: "darwin", appPath, resourcesPath: "/unused", packaged: true });
  assert.ok(packagedIcons.tray[0].endsWith("trayTemplate.png"));
});

test("non-macOS platforms keep the color tray candidates", () => {
  for (const platform of ["win32", "linux"] as const) {
    const icons = iconCandidates({ platform, appPath, resourcesPath: "/unused", packaged: false });
    assert.ok(icons.tray[0].endsWith(join("hicolor", "32x32", "apps", "reasonix-desktop.png")), platform);
    assert.ok(!icons.tray.some((path) => path.includes("trayTemplate")), platform);
  }
});
