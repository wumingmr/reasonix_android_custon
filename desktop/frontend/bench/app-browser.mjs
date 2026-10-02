#!/usr/bin/env node

import path from "node:path";
import { fileURLToPath } from "node:url";
import { startPreviewServer } from "./vite-preview-server.mjs";
import { newSessionButton, selectSession } from "./app-page-actions.mjs";

const frontendDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
process.env.PLAYWRIGHT_BROWSERS_PATH = !process.env.PLAYWRIGHT_BROWSERS_PATH || process.env.PLAYWRIGHT_BROWSERS_PATH === ".pw-browsers"
  ? path.join(frontendDir, ".pw-browsers")
  : process.env.PLAYWRIGHT_BROWSERS_PATH;
// Playwright reads PLAYWRIGHT_BROWSERS_PATH at module evaluation; import it
// only after the path normalization above.
const { chromium } = await import("playwright");
const port = Number(process.env.REASONIX_APP_BROWSER_PORT ?? 4657);
const preview = await startPreviewServer(frontendDir, port);
const browser = await chromium.launch({ headless: true });

function assert(condition, message) {
  if (!condition) throw new Error(message);
  process.stdout.write(`  PASS ${message}\n`);
}

async function settle(page, frames = 5) {
  await page.evaluate((count) => new Promise((resolve) => {
    const tick = () => --count <= 0 ? resolve() : requestAnimationFrame(tick);
    requestAnimationFrame(tick);
  }), frames);
}

try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  const pageErrors = [];
  page.on("pageerror", (error) => pageErrors.push(error.message));
  await page.goto(`http://127.0.0.1:${port}/?mock=bench&bench=1&app-lifecycle-probe=1`, { waitUntil: "domcontentloaded" });
  await page.locator("textarea.composer__input:not([aria-hidden=true])").waitFor();
  await page.locator(".project-tree").first().waitFor();
  await page.evaluate(() => {
    window.__appBrowserIdentity = {
      composer: document.querySelector("textarea.composer__input:not([aria-hidden=true])"),
      sidebar: document.querySelector(".sidebar"),
    };
  });
  const composer = page.locator("textarea.composer__input:not([aria-hidden=true])");
  await selectSession(page, "bench:small-6t");
  await page.waitForFunction(() => document.querySelector('.transcript')?.textContent?.includes('ASYNC LAYOUT EXPANSION COMPLETE'));
  await composer.fill("layout-owned draft");
  // Raw Markdown fallbacks become parsed DOM asynchronously. History preservation
  // means stable node identity, not identical transient textContent.
  const transcriptIdentity = () => {
    const nodes = [...document.querySelectorAll('[data-chat-anchor-key]')];
    window.__modelTranscriptNodes ??= nodes;
    return nodes.map((node, index) => ({ key: node.dataset.chatAnchorKey, kind: node.dataset.chatKind,
      sameHost: node === window.__modelTranscriptNodes[index] }));
  };
  const transcriptBeforeModel = await page.evaluate(transcriptIdentity);
  assert(transcriptBeforeModel.length > 0, 'model replay starts with hydrated transcript blocks');
  await page.locator('.modelsw__trigger:not(.effortsw__trigger)').click();
  const nextModel = page.locator('.modelsw__item[role="option"]:not([aria-selected="true"])').first();
  const nextModelName = await nextModel.locator('.modelsw__model').textContent();
  await nextModel.click();
  await page.waitForFunction(name => document.querySelector('.modelsw__label')?.textContent?.includes(name), nextModelName);
  await page.waitForFunction(() => document.querySelector('textarea.composer__input:not([aria-hidden=true])')?.disabled === false
    && window.__reasonixAppLifecycle?.snapshot().activeOperations === 0);
  const transcriptAfterModel = await page.evaluate(transcriptIdentity);
  const draftAfterModel = await composer.inputValue();
  assert(draftAfterModel === 'layout-owned draft' && JSON.stringify(transcriptAfterModel) === JSON.stringify(transcriptBeforeModel),
    'real model selection preserves source transcript, Composer draft and writable readiness');
  // A fresh session now seeds Overview in an expanded empty dock. Add Files
  // from the tab menu so the rest of the browser fixture can exercise the
  // workspace tree and preview.
  await page.getByRole('tab', { name: 'Overview', exact: true }).waitFor();
  assert(await page.getByRole('tab', { name: 'Overview', exact: true }).count() === 1,
    'fresh expanded workspace dock defaults to Overview');
  if (await page.getByRole('tab', { name: 'Files', exact: true }).count() === 0) {
    await page.locator('.workbench-dock__tab-add').click();
    await page.locator('.tab-add-menu__item', { hasText: 'Files' }).first().click();
  }
  await page.getByRole('tab', { name: 'Files', exact: true }).click();
  await page.locator('[data-workspace-path="README.md"]').click();
  await page.waitForFunction(() => document.querySelector('.workspace-preview__body')?.textContent?.includes('Browser-dev workspace preview.'));
  await page.evaluate(() => {
    Object.assign(window.__appBrowserIdentity, {
      workspace: document.querySelector('.workspace-panel'),
      workspaceTree: document.querySelector('.workspace-tree'),
      preview: document.querySelector('.workspace-preview__body'),
    });
  });

  assert(await page.locator(".app.app--workbench").count() === 1, "workbench layout renders from the authoritative startup snapshot");

  const identities = await page.evaluate(() => ({
    composer: window.__appBrowserIdentity.composer === document.querySelector("textarea.composer__input:not([aria-hidden=true])"),
    sidebar: window.__appBrowserIdentity.sidebar === document.querySelector(".sidebar"),
    workspace: window.__appBrowserIdentity.workspace === document.querySelector('.workspace-panel'),
    workspaceTree: window.__appBrowserIdentity.workspaceTree === document.querySelector('.workspace-tree'),
    preview: window.__appBrowserIdentity.preview === document.querySelector('.workspace-preview__body'),
  }));
  assert(Object.values(identities).every(Boolean), "management-page visits retain Sidebar, Composer, actual WorkspacePanel/tree and file preview identity");

  const terminalToggle = page.getByRole("button", { name: "Terminal", exact: true }).first();
  await terminalToggle.click();
  await page.locator('.terminal-drawer[aria-hidden="false"]').waitFor();
  assert(await page.locator('.terminal-drawer-resizer[tabindex="0"]').count() === 1, "open terminal drawer exposes one keyboard resizer");
  assert(await page.locator(".footer.footer--compact").count() === 1, "open terminal compacts the shared footer without remounting Composer");
  assert(await composer.inputValue() === "layout-owned draft", "terminal drawer lifecycle preserves the Composer draft");
  await terminalToggle.click();
  const closedTerminal = page.locator('.terminal-drawer[aria-hidden="true"][inert]');
  await closedTerminal.waitFor({ state: "attached" });
  await closedTerminal.waitFor({ state: "hidden" });
  assert(await closedTerminal.count() === 1, "closed warm terminal remains mounted and hidden");
  assert(await page.locator('.terminal-drawer-resizer[tabindex="-1"]').count() === 1, "closed warm terminal is inert and leaves keyboard navigation");

  await selectSession(page, "bench:geometry");
  await page.waitForFunction(() => document.querySelector(".transcript")?.textContent?.includes("Geometry contract fixture complete."));
  await selectSession(page, "bench:small-6t");
  await page.waitForFunction(() => document.querySelector(".transcript")?.textContent?.includes("ASYNC LAYOUT EXPANSION COMPLETE"));
  const afterSwitch = await page.evaluate(() => ({
    workspace: window.__appBrowserIdentity.workspace === document.querySelector('.workspace-panel'),
    workspaceTree: window.__appBrowserIdentity.workspaceTree === document.querySelector('.workspace-tree'),
    preview: window.__appBrowserIdentity.preview === document.querySelector('.workspace-preview__body'),
    selectedFile: document.querySelector('.workspace-tree__row--active')?.getAttribute('data-workspace-path'),
    subscriptions: window.__reasonixAppLifecycle?.snapshot().activeSubscriptions,
    operations: window.__reasonixAppLifecycle?.snapshot().activeOperations,
  }));
  assert(afterSwitch.workspace && afterSwitch.workspaceTree && afterSwitch.preview && afterSwitch.selectedFile === 'README.md',
    'same-project session switching preserves actual WorkspacePanel, tree, preview DOM and selected file');
  assert(afterSwitch.subscriptions === 6, `the six AppRuntimeEffects subscriptions remain singular (${afterSwitch.subscriptions})`);
  assert(afterSwitch.operations === 0, "instrumented operation owners report zero active operations (not yet all App operations)");

  // Browser-mock local switches exercise the renderer latency contract. Use
  // the navigation surface's paint receipt (the same click-to-first-paint
  // milestone reported in diagnostics), not completion of deferred Markdown
  // or lazy-content expansion after the first screen is already visible.
  const switchSamples = [];
  for (let index = 0; index < 20; index += 1) {
    const geometry = index % 2 === 0;
    const label = geometry ? "bench:geometry" : "bench:small-6t";
    // The latency gate stops at the first readable inline body. The full
    // ASYNC marker intentionally lives beyond the lazy-content preview and
    // is validated above; including its simulated 1.5s body fetch here would
    // benchmark deferred expansion rather than first readable paint.
    const marker = geometry ? "Geometry contract fixture complete." : "Asynchronously hydrated verification appendix";
    const previousIntent = await page.evaluate(() => window.__reasonixPerf?.stats().navigation?.intent ?? -1);
    await selectSession(page, label);
    await page.waitForFunction((text) => document.querySelector(".transcript")?.textContent?.includes(text), marker);
    await page.waitForFunction((intent) => {
      const navigation = window.__reasonixPerf?.stats().navigation;
      return navigation?.intent !== intent && navigation?.clickToFirstPaintMs !== undefined;
    }, previousIntent);
    switchSamples.push(await page.evaluate(() => window.__reasonixPerf.stats().navigation.clickToFirstPaintMs));
  }
  switchSamples.sort((a, b) => a - b);
  const localSwitchP95 = switchSamples[Math.ceil(switchSamples.length * 0.95) - 1];
  assert(localSwitchP95 <= 300,
    `browser-mock local click-to-first-paint P95 <= 300ms (${localSwitchP95.toFixed(1)}ms; samples=${switchSamples.map(value => value.toFixed(1)).join(",")})`);

  await page.locator('.project-tree__folder-main:has(svg.lucide-cloud)').click();
  await page.locator('.project-tree__topic-main:has-text("Remote demo session")').click();
  await page.locator('.remote-surface--ready').waitFor();
  await page.waitForFunction(() => document.querySelector('textarea.composer__input:not([aria-hidden=true])')?.disabled === false);
  assert((await page.locator('.topicbar').textContent()).includes('Remote demo session'), "remote project selection adopts its source workspace and authoritative hydrated surface");
  await newSessionButton(page).click();
  await page.waitForFunction(() => document.querySelector('.topicbar')?.textContent?.includes('New session'));
  await page.locator('.remote-surface--ready').waitFor();
  await page.waitForFunction(() => document.querySelector('textarea.composer__input:not([aria-hidden=true])')?.disabled === false);
  assert(await page.locator('.remote-surface').count() === 1, "global New Session stays on the remote workspace instead of opening a local blank");
  assert(await page.evaluate(() => window.__appBrowserIdentity.composer === document.querySelector('textarea.composer__input:not([aria-hidden=true])')), "local/remote navigation and remote New Session preserve the Composer DOM identity");
  await selectSession(page, "bench:geometry");
  await page.waitForFunction(() => document.querySelector('.transcript')?.textContent?.includes('Geometry contract fixture complete.'));
  assert(await page.locator('.remote-surface').count() === 0, "subsequent local navigation owns the surface; remote events do not reclaim it");
  const sentText = 'App source-bound submission fixture';
  await composer.fill(sentText);
  await composer.press('Enter');
  await page.locator('[data-chat-kind="user"]').filter({ hasText: sentText }).waitFor();
  await page.locator('.composer__btn--stop').click();
  await page.locator('.composer__btn--stop').waitFor({ state: 'hidden' });
  await page.waitForFunction(() => document.querySelector('textarea.composer__input:not([aria-hidden=true])')?.disabled === false);
  assert(await page.evaluate(() => window.__appBrowserIdentity.composer === document.querySelector('textarea.composer__input:not([aria-hidden=true])')),
    'ordinary source-bound send and native Stop preserve Composer identity and restore writable readiness');
  assert(pageErrors.length === 0, `layout replay emits no page errors (${pageErrors.length})`);

  process.stdout.write("app browser lifecycle gate passed\n");
} finally {
  await browser.close();
  await preview.close();
}
