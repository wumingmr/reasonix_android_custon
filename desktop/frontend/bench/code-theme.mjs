import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { build, preview, loadConfigFromFile } from "vite";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
process.env.PLAYWRIGHT_BROWSERS_PATH ||= path.join(root, ".pw-browsers");
const { chromium } = await import("playwright");
const outDir = await mkdtemp(path.join(tmpdir(), "reasonix-code-theme-"));
const { config } = await loadConfigFromFile({ command: "build", mode: "production" }, path.join(root, "vite.config.ts"));
let server, browser;
try {
  await build({ ...config, configFile: false, root, logLevel: "error",
    plugins: config.plugins.filter(plugin => !["archive-hidden-sourcemaps", "keep-dist-placeholder"].includes(plugin?.name)),
    build: { ...config.build, outDir, sourcemap: false,
      rolldownOptions: { ...config.build.rolldownOptions, input: path.join(root, "bench/code-theme.html") } } });
  server = await preview({ configFile: false, root, logLevel: "error", build: { outDir }, preview: { host: "127.0.0.1", port: 0 } });
  browser = await chromium.launch({ headless: true });
  const page = await browser.newPage({ viewport: { width: 1100, height: 900 }, permissions: ["clipboard-read", "clipboard-write"] });
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  await page.goto(`http://127.0.0.1:${server.httpServer.address().port}/bench/code-theme.html?mock=1`);
  const live = page.getByRole("region", { name: "Streaming code" });
  await live.locator(".hljs-keyword").first().waitFor();
  const prefix = await live.locator("pre").textContent();
  await live.locator(".code-block__line").first().evaluate(el => {
    el.dataset.retained = "yes";
    const range = document.createRange(); range.selectNodeContents(el);
    window.getSelection().removeAllRanges(); window.getSelection().addRange(range);
  });
  await page.getByRole("button", { name: "追加代码" }).click();
  await live.getByText("count", { exact: true }).count();
  await page.waitForFunction(() => document.querySelector('[aria-label="Streaming code"] pre').textContent.includes("42"));
  assert.equal(await live.locator(".code-block__line").first().getAttribute("data-retained"), "yes");
  assert.ok((await live.locator("pre").textContent()).startsWith(prefix));
  await page.getByRole("button", { name: "追加代码" }).click();
  await live.getByRole("button", { name: /Copy|复制/, exact: false }).click();
  const complete = await live.locator("pre").textContent();
  assert.equal(await page.evaluate(() => navigator.clipboard.readText()), complete, "copy excludes header and fence markers");
  await page.getByRole("button", { name: "追加代码" }).click();
  await page.waitForFunction(() => document.querySelector('[aria-label="Streaming code"] .hljs-keyword'));
  assert.equal(await live.locator("pre").textContent(), complete, "settlement preserves code text");

  async function checkContrast(label) {
    await page.locator('[aria-label="Completed code"] .hljs-string').first().waitFor();
    const colors = await page.locator('[aria-label="Completed code"] [class^="hljs-"]').evaluateAll(tokens =>
      [...new Set(tokens.map(token => getComputedStyle(token).color))]);
    assert.ok(colors.length >= 4, `${label}: syntax roles must actually have distinct colors, got ${colors}`);
    const measurements = await page.evaluate(() => {
      const luminance = color => {
        const rgb = color.match(/[\d.]+/g).slice(0, 3).map(Number).map(v => {
          v /= 255; return v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
        });
        return rgb[0] * .2126 + rgb[1] * .7152 + rgb[2] * .0722;
      };
      const ratio = (a, b) => (Math.max(a, b) + .05) / (Math.min(a, b) + .05);
      return [...document.querySelectorAll(".code-block--framed")].flatMap(block => {
        const pre = block.querySelector("pre");
        const header = block.querySelector(".code-block__header");
        return [...pre.querySelectorAll('[class^="hljs-"]'), pre, header, header.querySelector("button")].map(el => {
          const bg = getComputedStyle(el.closest("pre") ?? el).backgroundColor;
          return { token: el.className, bg, ratio: ratio(luminance(getComputedStyle(el).color), luminance(bg)) };
        });
      });
    });
    for (const value of measurements) {
      assert.ok(!value.bg.startsWith("rgba"), `${label} ${value.token}: code surface must be opaque`);
      assert.ok(value.ratio >= 4.49, `${label} ${value.token}: contrast ${value.ratio}`);
    }
    return Math.min(...measurements.map(value => value.ratio));
  }
  let minimum = Infinity;
  let cases = 0;
  for (const style of ["graphite", "aurora", "slate", "carbon", "nocturne", "amber"]) {
    await page.getByLabel("Style", { exact: true }).selectOption(style);
    for (const [mode, system] of [["light", "dark"], ["dark", "light"], ["auto", "light"], ["auto", "dark"]]) {
      await page.emulateMedia({ colorScheme: system });
      await page.getByLabel("Mode", { exact: true }).selectOption(mode);
      minimum = Math.min(minimum, await checkContrast(`${style}/${mode}/${system}`)); cases++;
    }
  }
  for (const pack of ["inverted", "midtone", "translucent", "wallpaper"]) {
    await page.getByLabel("Palette", { exact: true }).selectOption(pack);
    for (const mode of ["light", "dark"]) {
      await page.getByLabel("Mode", { exact: true }).selectOption(mode);
      minimum = Math.min(minimum, await checkContrast(`${pack}/${mode}`)); cases++;
    }
  }
  await page.getByLabel("Scene", { exact: true }).selectOption("home");
  minimum = Math.min(minimum, await checkContrast("wallpaper/home")); cases++;
  const spacing = await page.locator(".code-block--framed pre").first().evaluate(el => getComputedStyle(el).margin);
  assert.equal(spacing, "0px", "Markdown default pre margins cannot split the framed surface");
  await page.setViewportSize({ width: 390, height: 844 });
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth > innerWidth);
  assert.equal(overflow, false, "narrow viewport does not overflow horizontally");
  assert.deepEqual(errors, []);
  console.log(JSON.stringify({ cases, minimumContrast: minimum, streaming: "passed", copy: "passed", narrow: "passed", errors }));
} finally {
  await browser?.close();
  await new Promise(resolve => server ? server.httpServer.close(resolve) : resolve());
  await rm(outDir, { recursive: true, force: true });
}
