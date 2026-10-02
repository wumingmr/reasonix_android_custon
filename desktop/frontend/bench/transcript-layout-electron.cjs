const { app, BrowserWindow } = require("electron");
const path = require("node:path");

// A hidden, isolated native host exercises Chromium's real window/zoom path
// without activating or altering the developer's active desktop session.
app.setPath("userData", path.join(__dirname, "electron-profile"));
app.whenReady().then(async () => {
  if (process.platform === "darwin") app.dock.hide();
  const window = new BrowserWindow({
    show: false, focusable: false, width: 1920, height: 1080, useContentSize: true,
    webPreferences: { sandbox: true, contextIsolation: true, nodeIntegration: false, backgroundThrottling: false },
  });
  await window.loadURL(process.env.REASONIX_LAYOUT_URL);
});
app.on("window-all-closed", () => app.quit());
