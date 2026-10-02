import assert from "node:assert/strict";
import { appendFile, mkdir, mkdtemp, writeFile, copyFile, rm } from "node:fs/promises";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { build, preview, loadConfigFromFile } from "vite";
import {
  collectTranscriptPerformance,
  decideTranscriptPerformance,
  formatPerformanceSummary,
  installTranscriptPerformanceObserver,
  measureTranscriptPerformance,
  percentile,
} from "./transcript-performance.mjs";
import { launchTranscriptRetryHost } from "./transcript-retry.mjs";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
process.env.PLAYWRIGHT_BROWSERS_PATH = !process.env.PLAYWRIGHT_BROWSERS_PATH || process.env.PLAYWRIGHT_BROWSERS_PATH === ".pw-browsers"
  ? path.join(root, ".pw-browsers")
  : process.env.PLAYWRIGHT_BROWSERS_PATH;
// Playwright reads PLAYWRIGHT_BROWSERS_PATH at module evaluation.
const { chromium, webkit, _electron } = await import("playwright");
const outDir = await mkdtemp(path.join(tmpdir(), "reasonix-chat-build-"));
const evidence = process.env.REASONIX_CHAT_EVIDENCE ?? process.env.REASONIX_LAYOUT_ARTIFACTS ?? path.join(tmpdir(), "reasonix-chat-evidence");
await mkdir(evidence, { recursive: true });
const loaded = await loadConfigFromFile({ command: "build", mode: "production" }, path.join(root, "vite.config.ts"));
const config = loaded.config;
await build({ ...config, configFile: false, root, logLevel: "error",
  plugins: config.plugins.filter(plugin => !["archive-hidden-sourcemaps", "keep-dist-placeholder"].includes(plugin?.name)),
  build: { ...config.build, outDir, sourcemap: false,
    rolldownOptions: { ...config.build.rolldownOptions, input: path.join(root, "bench/chat-transcript.html") } } });
const server = await preview({ configFile: false, root, logLevel: "error", build: { outDir }, preview: { host: "127.0.0.1", port: 0 } });
const address = server.httpServer.address();
const reports = [];
const mode = process.env.REASONIX_TRANSCRIPT_MODE ?? (process.env.REASONIX_TRANSCRIPT_NATIVE_THUMB === "1" ? "native-scrollbar" : "headless-reader");
try {
  for (const [name, engine] of Object.entries(process.env.CHAT_BROWSER === "electron" ? { electron: _electron } : process.env.CHAT_BROWSER === "webkit" ? { webkit } : process.env.CHAT_BROWSER === "chromium" ? { chromium } : { chromium, webkit })) {
    let electronApp;
    const url = `http://127.0.0.1:${address.port}/bench/chat-transcript.html?mock=1`;
    if (name === "electron") {
      const main = path.join(outDir, "main.cjs");
      await copyFile(path.join(root, "bench/transcript-layout-electron.cjs"), main);
      electronApp = await engine.launch({ executablePath: createRequire(path.join(root, "../electron/package.json"))("electron"), args: [main], env: { ...process.env, REASONIX_LAYOUT_URL: url } });
    }
    const nativeThumb = process.env.REASONIX_TRANSCRIPT_NATIVE_THUMB === "1";
    const browser = electronApp ? undefined : await engine.launch({ headless: !nativeThumb });
    const report = { browser: name, complete: false, samples: [], errors: [],
      version: browser?.version() ?? await electronApp.evaluate(() => process.versions.electron),
      platform: process.platform, arch: process.arch };
    const writeAttempt = (scenario, attempt, decision = null) => writeFile(path.join(evidence, `${scenario}-attempt-${attempt.attempt}.json`), JSON.stringify({
      browser: report.browser,
      version: report.version,
      platform: report.platform,
      arch: report.arch,
      mode,
      ...attempt,
      decision,
    }, null, 2));
    reports.push(report);
    try {
      const page = electronApp ? await electronApp.firstWindow() : await browser.newPage({ viewport: { width: 1280, height: 900 } });
      const errors = report.errors;
      await page.addInitScript(() => { window.chatWrites = []; window.__REASONIX_TRANSCRIPT_SCROLL_WRITE__ = write => { window.chatWrites.push(write); if (window.chatWrites.length > 100) window.chatWrites.shift(); }; });
      await installTranscriptPerformanceObserver(page);
      page.on("pageerror", error => { errors.push(error.message); console.error(error.stack); });
      page.on("console", message => { if (/Maximum update depth|ResizeObserver loop|Encountered two children with the same key/.test(message.text())) errors.push(message.text()); });
      await page.goto(url);
      await page.locator(".chat-column .md h3").last().waitFor();
      await page.evaluate(() => document.fonts.ready);
      const scroll = page.locator(".chat-flow-scroll");
      const settleFrames = target => target.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
      const frame = () => settleFrames(page);
      const navigate = async key => {
        const mark = page.locator(`[data-nav-turn="${key}"]`);
        await mark.focus(); await mark.press("Enter");
      };
      const bottom = () => scroll.evaluate(el => el.scrollHeight - el.scrollTop - el.clientHeight);
      await frame();
      await page.waitForFunction(() => { const el = document.querySelector(".chat-flow-scroll"); return el.scrollHeight - el.scrollTop - el.clientHeight <= 24; }, null, { timeout: 5000 });
      assert.ok(await bottom() <= 24, "initially follows latest");
      const rail = page.locator('.dsh-TurnNavigator-frame');
      await rail.waitFor();
      assert.equal(await page.locator('[data-nav-turn]').count(), 20, 'rail lists loaded turns only');
      const activeMark = page.locator('[data-nav-turn][aria-current="true"]');
      assert.equal(await activeMark.evaluate(el => getComputedStyle(el, '::before').backgroundColor),
        await page.locator('.chat-surface').evaluate(el => {
          const probe = document.createElement('span');
          probe.style.backgroundColor = 'var(--accent)'; el.append(probe);
          const value = getComputedStyle(probe).backgroundColor; probe.remove(); return value;
        }), 'active rail mark uses the v1.38.7 accent');
      const railBox = await rail.boundingBox();
      await page.mouse.move(railBox.x + 18, railBox.y + 196);
      await page.locator('.dsh-TurnNavigator-preview').waitFor();
      assert.match(await page.locator('.dsh-TurnNavigator-preview').innerText(), /Question 20/);
      assert.equal(await page.locator('[data-nav-turn="u19"]').getAttribute('data-preview-distance'), '0', 'hovered mark owns the accent');
      assert.equal(await page.locator('[data-nav-turn="u18"]').getAttribute('data-preview-distance'), '1', 'adjacent mark uses the first v1.38.7 fade');
      assert.equal(await page.locator('[data-nav-turn="u17"]').getAttribute('data-preview-distance'), '2', 'second adjacent mark uses the second v1.38.7 fade');
      await page.screenshot({ path: path.join(evidence, `${name}-turn-navigation.png`) });
      await page.mouse.click(railBox.x + 18, railBox.y + 196);
      await frame();
      assert.equal(await scroll.getAttribute('data-scroll-mode'), 'reader', 'rail click enters reading intent');
      await navigate('u0'); await frame();
      assert.equal(await page.locator('[data-nav-turn="u0"]').getAttribute('aria-current'), 'true', 'keyboard navigation updates the active tick');
      await page.locator('.chat-to-bottom').click(); await frame();
      if (nativeThumb) {
        await scroll.evaluate(el => {
          window.chatThumbEvents = [];
          for (const type of ["pointerdown", "pointerup", "scroll"]) el.addEventListener(type, event => window.chatThumbEvents.push({ type, top: el.scrollTop, target: event.target === el }));
        });
        const track = await scroll.evaluate(el => { const box = el.getBoundingClientRect();
          const gutter = el.offsetWidth - el.clientWidth;
          const arrow = gutter;
          const trackHeight = Math.max(1, box.height - 2 * arrow);
          const thumbHeight = Math.max(gutter, trackHeight * el.clientHeight / el.scrollHeight);
          return {
            x: box.right - gutter / 2,
            top: box.top,
            bottom: box.bottom,
            before: el.scrollTop,
            gutter,
            thumbHeight,
            thumbCenter: box.bottom - arrow - thumbHeight / 2,
          }; });
        report.nativeThumb = { track };
        // This coordinate-based variant requires an exposed native gutter (for
        // example headed Linux/Xvfb). A hidden macOS overlay can select text
        // instead; that must never count as a successful scrollbar drag.
        assert.ok(track.gutter > 0, "native scrollbar gutter unavailable; run the native-thumb variant in an isolated host with visible scrollbars");
        // Chromium's Linux scrollbar reserves an arrow-button-sized region at
        // each end. Start in the computed thumb center instead of the bottom
        // arrow, then drag toward the middle of the track.
        await page.mouse.move(track.x, track.thumbCenter); await page.mouse.down();
        await page.mouse.move(track.x, (track.top + track.bottom) / 2, { steps: 20 }); await page.mouse.up();
        report.nativeThumb = { track, after: await scroll.evaluate(el => ({ top: el.scrollTop, mode: el.dataset.scrollMode, events: window.chatThumbEvents })) };
        await page.screenshot({ path: path.join(evidence, `${name}-native-thumb.png`) });
        await page.waitForFunction(before => document.querySelector(".chat-flow-scroll").scrollTop < before - 100, track.before);
        assert.equal(await scroll.getAttribute("data-scroll-mode"), "reader", "native scrollbar dragging releases follow");
        await page.locator(".chat-to-bottom").click(); await frame();
      }
      await scroll.hover();
      const beforeWheel = await scroll.evaluate(el => el.scrollTop);
      await page.mouse.wheel(0, -600);
      await page.waitForFunction(() => !document.querySelector(".chat-to-bottom").hidden);
      // WebKit dispatches wheel input before its native scroll animation ends.
      // Measure content-induced drift only after that user movement settles.
      await page.waitForFunction(before => document.querySelector(".chat-flow-scroll").scrollTop < before - 1, beforeWheel);
      await scroll.evaluate(el => new Promise(resolve => {
        let previous = el.scrollTop, stable = 0;
        const sample = () => {
          const current = el.scrollTop;
          stable = Math.abs(current - previous) < 0.1 ? stable + 1 : 0;
          previous = current;
          if (stable >= 6) resolve(); else requestAnimationFrame(sample);
        };
        requestAnimationFrame(sample);
      }));
      await frame();
      const anchor = await page.evaluate(() => {
        const el = document.querySelector(".chat-flow-scroll"), top = el.getBoundingClientRect().top;
        const row = [...document.querySelectorAll("[data-chat-anchor-key]")].find(row => row.childNodes.length && row.getBoundingClientRect().bottom > top + 1);
        return { key: row.dataset.chatAnchorKey, top: row.getBoundingClientRect().top };
      });
      for (let i = 0; i < 60; i++) { await page.evaluate(i => window.chatFixture.tick(i), i); await frame(); }
      const topAfter = await page.locator(`[data-chat-anchor-key="${anchor.key}"]`).evaluate(el => el.getBoundingClientRect().top);
      assert.ok(Math.abs(topAfter - anchor.top) <= 2, `stream anchor drift ${topAfter - anchor.top}`);
      await page.evaluate(() => window.chatFixture.prepend()); await frame();
      const topPrepended = await page.locator(`[data-chat-anchor-key="${anchor.key}"]`).evaluate(el => el.getBoundingClientRect().top);
      assert.ok(Math.abs(topPrepended - anchor.top) <= 2, `prepend anchor drift ${topPrepended - anchor.top}`);
      await page.locator(".chat-to-bottom").click(); await frame();
      await page.waitForFunction(() => { const el = document.querySelector(".chat-flow-scroll"); return el.scrollHeight - el.scrollTop - el.clientHeight <= 24; }, null, { timeout: 5000 });
      assert.ok(await bottom() <= 24, "return to latest resumes follow");
      await page.locator('[data-chat-anchor-key="a19"] .md p').first().waitFor();
      const selected = await page.locator('[data-chat-anchor-key="a19"] .md p').first().evaluate(el => {
        window.selectedChatParagraph = el;
        const range = document.createRange(); range.selectNodeContents(el);
        getSelection().removeAllRanges(); getSelection().addRange(range); return getSelection().toString();
      });
      await page.evaluate(() => window.chatFixture.settle()); await frame();
      assert.ok(await page.evaluate(() => window.selectedChatParagraph.isConnected), "settlement retains the selected paragraph host");
      assert.equal(await page.evaluate(() => getSelection().toString()), selected, "native selection survives stream settlement");
      await navigate("u19"); await frame();
      await page.locator('[data-chat-kind="process"] button').last().click();
      await page.locator(".chat-tool [data-disclosure-row]").last().click();
      await page.locator(".dsh-ToolRow-inspectButton").last().click();
      await page.locator('[role="dialog"]').waitFor();
      await page.screenshot({ path: path.join(evidence, `${name}-details.png`) });
      await page.keyboard.press("Escape");
      assert.equal(await page.locator('[role="dialog"]').count(), 0);
      assert.ok(await page.locator(".dsh-ToolRow-inspectButton").last().evaluate(el => el === document.activeElement), "drawer restores trigger focus after removing inert");
      const input = page.locator("textarea.composer__input:not(.composer__input--measure)");
      assert.deepEqual(errors, [], "browser errors before performance sampling");
      report.performance = [];
      for (const turns of [240, 1000]) {
        const scenario = `${name}-${mode}-${turns}`;
        let attempts = [];
        let firstTraceActive = true;
        let retryHost;
        await page.context().tracing.start({ screenshots: true, snapshots: true });
        try {
          const collected = await collectTranscriptPerformance(async attempt => {
            if (attempt === 1) {
              const first = await measureTranscriptPerformance({ page, turns, attempt, frame: settleFrames, errors });
              await writeAttempt(scenario, first);
              return first;
            }
            if (attempt === 2) {
              await page.screenshot({ path: path.join(evidence, `${scenario}-first-limit-exceedance.png`) });
              await page.context().tracing.stop({ path: path.join(evidence, `${scenario}-first-limit-exceedance.zip`) });
              firstTraceActive = false;
            }
            assert.ok(browser, "bounded transcript retry requires an isolated browser process");
            const retryErrors = [];
            // Keep the primary page alive for the functional checks below, but
            // do not make retries compete with its fully mounted 1000-turn
            // transcript. A browser context is storage isolation, not process
            // or scheduler isolation, so retries need a separate browser.
            retryHost ??= await launchTranscriptRetryHost(engine, {
              headless: !nativeThumb,
              viewport: { width: 1280, height: 900 },
            });
            return retryHost.run(async (retryPage, context) => {
              await installTranscriptPerformanceObserver(retryPage);
              retryPage.on("pageerror", error => { retryErrors.push(error.message); console.error(error.stack); });
              retryPage.on("console", message => { if (/Maximum update depth|ResizeObserver loop/.test(message.text())) retryErrors.push(message.text()); });
              await context.tracing.start({ screenshots: true, snapshots: true });
              try {
                await retryPage.goto(url);
                await retryPage.locator(".chat-column .md h3").last().waitFor();
                await retryPage.evaluate(() => document.fonts.ready);
                const sample = await measureTranscriptPerformance({ page: retryPage, turns, attempt, frame: settleFrames, errors: retryErrors });
                await writeAttempt(scenario, sample);
                await context.tracing.stop();
                return sample;
              } catch (error) {
                await context.tracing.stop({ path: path.join(evidence, `${scenario}-attempt-${attempt}-functional-failure.zip`) });
                throw error;
              }
            });
          });
          attempts = collected.attempts;
          if (firstTraceActive) {
            await page.context().tracing.stop();
            firstTraceActive = false;
          }
        } catch (error) {
          if (firstTraceActive) await page.context().tracing.stop({ path: path.join(evidence, `${scenario}-functional-failure.zip`) });
          await writeFile(path.join(evidence, `${scenario}-failure.json`), JSON.stringify({ error: String(error), attempts }, null, 2));
          throw error;
        } finally {
          await retryHost?.close();
        }
        const decision = decideTranscriptPerformance(attempts);
        for (const attempt of attempts) await writeAttempt(scenario, attempt, decision);
        report.samples.push(...attempts);
        report.performance.push({ turns, decision, attempts });
        assert.ok(decision.passed, `${turns} turns ${decision.status}: ${JSON.stringify({
          longTaskMedians: decision.medians,
          inputP95Median: decision.inputP95Median,
        })}`);
        await page.evaluate(() => window.chatFixture.settle());
      }
      await frame();
      await input.fill("");
      await navigate("u998"); await frame();
      await page.screenshot({ path: path.join(evidence, `${name}-chat.png`) });
      let expanded;
      if (process.env.CHAT_EXPANDED === "1") {
        const started = Date.now();
        await page.evaluate(() => {
          document.querySelectorAll('[data-chat-kind="process"] button[aria-expanded="false"]').forEach(button => button.click());
        }); await frame();
        await page.evaluate(() => document.querySelectorAll('.chat-reasoning [data-disclosure-row][aria-expanded="false"]').forEach(button => button.click()));
        await frame();
        const beforeTasks = await page.evaluate(() => window.chatMetrics.tasks.length);
        for (let turn = 0; turn < 1000; turn += 20) {
          await navigate(`u${turn}`);
          await page.waitForFunction(() => window.chatFixture.pending() === 0); await frame();
          await page.evaluate(() => document.querySelectorAll('.chat-code-fold button[aria-expanded="false"]').forEach(button => button.click()));
        }
        await page.locator('[data-chat-anchor-key="tool980"] .chat-tool [data-disclosure-row]').click();
        await page.locator('[data-chat-anchor-key="tool980"] .dsh-ToolRow-inspectButton').click();
        await page.locator(".chat-details__body > .btn").first().click(); await frame();
        expanded = { elapsedMs: Date.now() - started, dom: await page.locator("*").count(),
          tasks: await page.evaluate(start => window.chatMetrics.tasks.slice(start), beforeTasks),
          heap: await page.evaluate(() => performance.memory?.usedJSHeapSize) };
        await page.keyboard.press("Escape");
      }
      const soakSeconds = Number(process.env.CHAT_SOAK_SECONDS ?? 0);
      if (soakSeconds > 0) {
        await page.evaluate(() => window.chatFixture.reset(240)); await frame();
        for (let pageIndex = 0; pageIndex < 3; pageIndex++) { await page.evaluate(() => window.chatFixture.older()); await frame(); }
        const started = Date.now(); let cycles = 0;
        while (Date.now() - started < soakSeconds * 1000) {
          await page.evaluate(index => window.chatFixture.tick(index % 40), cycles);
          await input.press("s"); await input.press("Backspace");
          if (cycles % 10 === 0) {
            await navigate("u238");
            await page.locator('[data-chat-kind="process"][data-chat-turn="u238"] > button').click();
          }
          if (cycles % 10 === 5) { await scroll.hover(); await page.mouse.wheel(0, -120); }
          if (cycles % 20 === 19) { await page.evaluate(() => window.chatFixture.switchSession()); await frame(); }
          cycles++;
        }
        await page.evaluate(() => window.chatFixture.settle()); await frame();
        report.soak = { elapsedMs: Date.now() - started, cycles, dom: await page.locator("*").count() };
        assert.equal(await input.inputValue(), "", "continuous streaming never loses or duplicates real input");
      }
      const switches = [];
      await page.evaluate(() => window.chatFixture.reset(60)); await frame();
      const cdp = name === "chromium" ? await page.context().newCDPSession(page) : undefined;
      const heap = async () => { if (!cdp) return undefined; await cdp.send("HeapProfiler.collectGarbage"); return (await cdp.send("Runtime.getHeapUsage")).usedSize; };
      const baseline = await heap();
      for (let index = 0; index < 20; index++) {
        const duration = await page.evaluate(() => new Promise(resolve => {
          const start = performance.now(); window.chatFixture.switchSession(); requestAnimationFrame(() => requestAnimationFrame(() => resolve(performance.now() - start)));
        })); switches.push(duration);
      }
      const released = await heap();
      const heapGrowth = released === undefined ? undefined : released - baseline;
      assert.ok(percentile(switches) <= 300, `session switch P95 ${percentile(switches)}`);
      if (heapGrowth !== undefined) assert.ok(heapGrowth <= 20 * 1024 * 1024, `released heap growth ${heapGrowth}`);
      await page.waitForFunction(() => window.chatFixture.pending() === 0);
      await frame(); await frame();
      // IntersectionObserver can admit the final visible parse after a transient
      // zero-pending snapshot. Require bounded quiescence first; a layout loop
      // can never satisfy this condition and fails the five-second deadline.
      await page.waitForFunction(() => {
        const last = window.chatWrites.at(-1);
        const stamp = `${last?.generation}:${last?.transaction}`;
        if (window.chatIdle?.stamp !== stamp || window.chatFixture.pending()) window.chatIdle = { stamp, at: performance.now() };
        return performance.now() - window.chatIdle.at >= 250;
      }, null, { timeout: 5000 });
      const writesBeforeIdle = await page.evaluate(() => window.chatWrites.at(-1)?.transaction);
      await page.waitForTimeout(1000);
      const writesAfterIdle = await page.evaluate(() => window.chatWrites.at(-1)?.transaction);
      assert.equal(writesAfterIdle, writesBeforeIdle, "settled layout queue converges");
      await page.evaluate(() => window.chatFixture.authored());
      await page.getByText("我是 Reasonix。", { exact: true }).waitFor();
      const authoredText = await page.locator(".chat-column").innerText();
      assert.match(authoredText, /你是谁/);
      assert.match(authoredText, /旧会话问题/);
      assert.match(authoredText, /<response-language>用户引用的 XML<\/response-language>/);
      assert.doesNotMatch(authoredText, /private environment|internal policy|legacy internal route|session-context|capability-route/);
      await page.screenshot({ path: path.join(evidence, `${name}-authored-chat.png`) });
      await page.evaluate(() => window.chatFixture.maintenance("start"));
      const maintenanceCard = page.locator('[data-entrance="maintenance:bench-maintenance"]');
      await maintenanceCard.waitFor();
      assert.match(await maintenanceCard.innerText(), /Compacting conversation/);
      assert.equal(await page.locator('.chat-process').getAttribute('aria-expanded'), 'false', 'maintenance remains visible outside the previous folded process');
      await maintenanceCard.evaluate(el => { window.maintenanceCardNode = el; });
      await page.evaluate(() => window.chatFixture.maintenance("refresh")); await frame();
      assert.match(await maintenanceCard.innerText(), /Compacting conversation/);
      assert.doesNotMatch(await maintenanceCard.innerText(), /interrupted/i);
      assert.equal(await maintenanceCard.count(), 1, 'history and runtime restore one operation card');
      await page.evaluate(() => window.chatFixture.maintenance("completed")); await frame();
      assert.equal(await page.evaluate(() => window.maintenanceCardNode.isConnected), true, 'terminal update preserves the card host');
      await maintenanceCard.getByRole('button').click();
      assert.match(await maintenanceCard.innerText(), /Durable compression summary/);
      await page.screenshot({ path: path.join(evidence, `${name}-maintenance-restored.png`) });
      await page.evaluate(() => window.chatFixture.maintenance("unknown")); await frame();
      assert.match(await maintenanceCard.innerText(), /could not be fully restored/);
      assert.doesNotMatch(await maintenanceCard.innerText(), /Context compacted/);
      report.maintenance = { restored: true, uniqueCard: true, summaryExpanded: true, unknownSafe: true };
      await page.evaluate(() => window.chatFixture.weather());
      await page.locator('[data-chat-anchor-key="weather-final"] table').waitFor();
      const presented = page.locator('.presented-files');
      await presented.waitFor();
      const changed = page.locator('.turn-files');
      await changed.waitFor();
      assert.equal(await presented.locator('.presented-file').count(), 2, 'trusted present result renders one card per file');
      assert.equal(await presented.locator('.presented-file__split').count(), 2, 'every presented file exposes one split open control');
      assert.match(await presented.innerText(), /shanghai-weather\.html/);
      assert.match(await presented.innerText(), /weather-notes\.md/);
      assert.match(await changed.innerText(), /Edited 2 files/);
      assert.match(await changed.innerText(), /\+5/);
      assert.match(await changed.innerText(), /−1/);
      await changed.locator('.turn-files__list button').first().click();
      assert.match(await changed.locator('[role="status"]').innerText(), /Diff details for this turn are unavailable/, 'disconnected fixture reports missing Diff service instead of swallowing clicks');
      assert.equal(await page.locator('.chat-older').count(), 0, 'standalone fixture must not offer nonexistent history');
      await presented.locator('.presented-file__more').first().click();
      const menu = page.getByRole('menu');
      assert.match(await menu.innerText(), /Open in built-in browser/);
      assert.equal(await menu.getByRole('menuitem').last().evaluate(el => {
        const rect = el.getBoundingClientRect();
        return el.contains(document.elementFromPoint(rect.left + rect.width / 2, rect.top + rect.height / 2));
      }), true, 'bottom action remains clickable beyond the transcript scroll area');
      await page.keyboard.press('End');
      assert.equal(await menu.getByRole('menuitem').last().evaluate(el => el === document.activeElement), true);
      await page.keyboard.press('Escape');
      assert.equal(await presented.locator('.presented-file__more').first().evaluate(el => el === document.activeElement), true, 'Escape restores trigger focus');
      await presented.scrollIntoViewIfNeeded();
      await page.screenshot({ path: path.join(evidence, `${name}-presented-files.png`) });
      await frame();
      await scroll.hover();
      await page.mouse.wheel(0, -500);
      await frame();
      const processToggle = page.locator('.chat-process');
      assert.equal(await processToggle.getAttribute('aria-expanded'), 'false', 'recovered weather turn folds');
      assert.equal(await page.locator('.chat-tool').count(), 0, 'collapsed process does not mount tool bodies');
      assert.match(await processToggle.innerText(), /6/);
      await page.screenshot({ path: path.join(evidence, `${name}-weather-collapsed.png`) });
      await processToggle.click();
      await page.locator('[data-chat-anchor-key="weather-search"]').scrollIntoViewIfNeeded();
      const weatherRows = await page.locator('.chat-tool [data-disclosure-row]').evaluateAll(rows => rows.map(row => ({ height: row.getBoundingClientRect().height, text: row.textContent })));
      assert.ok(weatherRows.every(row => row.height <= 32), 'tools use compact single-line rows');
      assert.ok(weatherRows.some(row => row.text.includes('获取并核对上海天气')), 'description replaces line counts');
      assert.equal(await page.locator('.dsh-ContextInjectionRow-body').count(), 0, 'permission record starts collapsed');
      await page.screenshot({ path: path.join(evidence, `${name}-weather-expanded.png`) });
      await page.locator('[data-chat-anchor-key="weather-bash"] [data-disclosure-row]').click();
      await page.locator('[data-terminal]').waitFor();
      await page.screenshot({ path: path.join(evidence, `${name}-weather-terminal.png`) });
      await scroll.hover();
      await page.mouse.wheel(0, -500);
      await frame();
      await page.locator('[data-chat-anchor-key="weather-search"] [data-disclosure-row]').click();
      await page.locator('[data-web="search"]').waitFor();
      assert.equal(await page.locator('[data-web="search"] a').count(), 1, 'web sources retain safe host links');
      report.weatherRows = weatherRows;
      await page.evaluate(() => window.chatFixture.backgroundLaunch()); await frame();
      await page.locator('.chat-process').click(); await frame();
      assert.equal(await page.locator('.chat-tool[data-state="done"]').count(), 2, 'background launch receipts settle for PowerShell and Bash');
      assert.equal(await page.locator('.chat-tool [data-state="ongoing"]').count(), 0, 'settled launches have no perpetual running animation');
      await page.evaluate(() => window.chatFixture.backgroundOutputHistory()); await frame();
      await page.locator('.chat-process').click(); await frame();
      assert.equal(await page.locator('.chat-tool[data-state="done"]').count(), 4, 'restored output reads settle independently of sampled job state');
      assert.equal(await page.locator('.chat-tool [data-state="ongoing"], .chat-tool[data-state="error"], .chat-tool[data-state="stopped"]').count(), 0, 'job snapshots cannot restart or fail a completed output read');
      await page.evaluate(() => window.chatFixture.toolAliasRegression()); await frame();
      const aliasProcess = page.locator('.chat-process');
      assert.equal(await aliasProcess.getAttribute('aria-expanded'), 'false', 'recovered alias regression folds after completion');
      await aliasProcess.click(); await frame();
      assert.equal(await page.locator('[data-chat-anchor-key="alias-call"]').count(), 1, 'formal result and event alias mount one tool DOM node');
      const aliasOrder = await page.locator('[data-chat-anchor-key]').evaluateAll(nodes => nodes.map(node => node.getAttribute('data-chat-anchor-key')));
      assert.ok(aliasOrder.indexOf('alias-call') < aliasOrder.indexOf('alias-final'), 'tool DOM node remains before the final answer');
      for (const order of ['record-first', 'event-first']) {
        for (const count of [1, 2]) {
          await page.evaluate(({ order, count }) => window.chatFixture.steer(order, count), { order, count }); await frame();
          assert.equal(await page.locator('[data-chat-anchor-key^="he:m:steer-"]').count(), count,
            `${order}: one DOM record per insertion, including equal-text insertions`);
          assert.equal(await page.locator('.dsh-ContextInjectionRow-root').count(), count,
            `${order}: steer event does not add another system record`);
        }
      }
      for (const order of ['record-first', 'event-first']) {
        for (const count of [1, 2]) {
          await page.evaluate(({ order, count }) => window.chatFixture.unappliedSteer(order, count), { order, count }); await frame();
          assert.equal(await page.locator('[data-chat-anchor-key^="he:m:unapplied-"]').count(), count,
            `${order}: one DOM warning per unapplied insertion`);
        }
      }
      assert.deepEqual(errors, []);
      Object.assign(report, { complete: true, expanded, switches, switchP95: percentile(switches), heapGrowth, anchorDrift: topAfter - anchor.top, prependDrift: topPrepended - anchor.top });
      console.log(JSON.stringify({
        ...report,
        samples: report.samples.map(sample => ({
          ...sample,
          phases: Object.fromEntries(Object.entries(sample.phases).map(([phase, value]) => [phase, { ...value, longTasks: undefined }])),
          inputs: undefined,
        })),
        performance: report.performance.map(sample => ({ turns: sample.turns, decision: sample.decision, attempts: sample.attempts.length })),
      }));
    } catch (error) {
      report.failure = String(error); throw error;
    } finally { await browser?.close(); await electronApp?.close(); }
  }
} finally {
  await server.httpServer.close();
  await writeFile(path.join(evidence, `${process.env.CHAT_BROWSER ?? "browsers"}-results.json`), JSON.stringify(reports, null, 2));
  if (process.env.GITHUB_STEP_SUMMARY) {
    const lines = reports.flatMap(report => (report.performance ?? []).map(sample => formatPerformanceSummary(report.browser, sample.turns, sample.decision, sample.attempts)));
    if (lines.length) await appendFile(process.env.GITHUB_STEP_SUMMARY, `\n### Transcript performance (${mode})\n\n${lines.join("\n")}\n`);
  }
  await rm(outDir, { recursive: true, force: true });
}
