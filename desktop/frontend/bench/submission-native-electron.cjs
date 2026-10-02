const { app, BrowserWindow } = require("electron");
const path = require("node:path");

if (process.platform !== "linux" || !process.env.DISPLAY) throw new Error("Requires isolated Linux/Xvfb");
app.setPath("userData", path.join(__dirname, "electron-profile"));
app.whenReady().then(async () => {
  const window = new BrowserWindow({
    show: true, frame: false, x: 0, y: 0, width: 1280, height: 900, useContentSize: true,
    title: "Reasonix Handoff Native",
    webPreferences: { sandbox: true, contextIsolation: true, nodeIntegration: false, backgroundThrottling: false },
  });
  window.on("page-title-updated", event => event.preventDefault());
  await window.loadURL(process.env.REASONIX_LAYOUT_URL);
});
app.on("window-all-closed", () => app.quit());
