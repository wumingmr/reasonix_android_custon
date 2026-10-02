// Real-browser click verification for answer file references and the SVG code
// block. Proves the DOM behavior the tsx suites can only simulate: a verified
// path becomes a clickable reference that commits a preview command into the
// running navigation owner, an unverified path stays plain text, and the SVG
// block actually renders a picture from the sanitized source with a working
// preview/source toggle.
import assert from "node:assert/strict";
import { createServer } from "vite";
import path from "node:path";
import { fileURLToPath } from "node:url";

const frontendDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
process.env.PLAYWRIGHT_BROWSERS_PATH = !process.env.PLAYWRIGHT_BROWSERS_PATH || process.env.PLAYWRIGHT_BROWSERS_PATH === ".pw-browsers"
  ? path.join(frontendDir, ".pw-browsers")
  : process.env.PLAYWRIGHT_BROWSERS_PATH;
const { chromium } = await import("playwright");

const server = await createServer({
  root: frontendDir,
  server: { host: "127.0.0.1", port: 0, hmr: false },
  logLevel: "error",
  plugins: [{
    name: "chat-file-reference-fixture",
    configureServer(dev) {
      dev.middlewares.use(async (req, res, next) => {
        if (req.url !== "/chat-file-reference-fixture") return next();
        res.setHeader("Content-Type", "text/html");
        res.end(await dev.transformIndexHtml(req.url, '<html><head><link rel="icon" href="data:,"></head><body><div id="root"></div><script type="module" src="/bench/chat-file-reference-fixture.tsx"></script></body></html>'));
      });
    },
  }],
});
await server.listen();

let browser;
try {
  const address = server.httpServer.address();
  browser = await chromium.launch({ headless: true, executablePath: process.env.CHROME_EXECUTABLE });
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  const errors = [];
  page.on("pageerror", error => { errors.push(error.message); console.error(error.message); });
  page.on("console", message => { if (message.type() === "error") { errors.push(message.text()); console.error(message.text()); } });
  await page.goto(`http://127.0.0.1:${address.port}/chat-file-reference-fixture`);

  // The verified reference is clickable and names the host's display path.
  const reference = page.locator("button.md-code--presented-file");
  await reference.waitFor({ timeout: 10_000 });
  assert.equal(await reference.getAttribute("title"), "out/diagram.svg", "reference names the verified display path");
  assert.match(await page.locator(".md").first().innerText(), /\/repo\/out\/missing\.svg/, "the answer text is preserved");
  assert.equal(await page.locator("button.md-code--presented-file").count(), 1, "an unverified path stays text");
  console.log("PASS verified reference is the only clickable path");

  await reference.click();
  await page.waitForFunction(() => document.getElementById("request")?.textContent !== "", null, { timeout: 10_000 });
  const request = JSON.parse(await page.locator("#request").textContent());
  assert.equal(request.source, "reference", "the click enters the navigation lifecycle as a reference");
  assert.equal(request.action, "preview", "the default click previews");
  assert.equal(request.path, "out/diagram.svg", "the preview targets the verified display path");
  console.log("PASS click commits a reference preview command");

  // The SVG fence renders a picture built from the sanitized bytes.
  const image = page.locator(".md-svg__preview img");
  await image.waitFor({ timeout: 10_000 });
  const source = await image.getAttribute("src");
  assert.match(source, /^(blob:|data:image\/svg\+xml)/, "the preview loads sanitized bytes as an image source");
  const box = await image.boundingBox();
  assert(box && box.width > 0 && box.height > 0, "the picture is laid out with real geometry");
  // A malformed document still yields a sized <img> element, so prove the
  // browser decoded the picture instead of showing a broken-image placeholder.
  const decoded = await page.evaluate(async () => {
    const img = document.querySelector(".md-svg__preview img");
    if (!(img instanceof HTMLImageElement)) return null;
    try { await img.decode(); } catch { return "decode-failed"; }
    return img.naturalWidth > 0 && img.naturalHeight > 0 ? "decoded" : "empty";
  });
  assert.equal(decoded, "decoded", "the browser decoded the sanitized SVG as an image");
  assert(box.height <= 32 * 16 + 1, "the preview stays inside its height ceiling");
  assert.equal(await page.locator(".md-svg__note").count(), 0, "a previewable SVG shows no fallback note");
  console.log(`PASS svg fence renders a ${Math.round(box.width)}x${Math.round(box.height)} picture`);

  // Preview / source toggle, and copy keeps the original text.
  await page.getByRole("button", { name: /source|源码|原始碼/i }).click();
  await page.locator(".md-svg .code-block").waitFor({ timeout: 10_000 });
  const code = await page.locator(".md-svg .code-block").innerText();
  assert.match(code, /linearGradient/, "the source view shows the original SVG");
  assert.equal(await page.locator(".md-svg__preview img").count(), 0, "source mode replaces the picture");
  await page.getByRole("button", { name: /preview|预览|預覽/i }).click();
  await page.locator(".md-svg__preview img").waitFor({ timeout: 10_000 });
  console.log("PASS preview and source toggle both ways");

  assert.deepEqual(errors, [], "no page errors");
  console.log("PASS no page errors");
} finally {
  await browser?.close();
  await server.close();
}
