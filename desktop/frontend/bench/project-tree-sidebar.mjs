import assert from "node:assert/strict";
import { startPreviewServer } from "./vite-preview-server.mjs";
import { selectSession } from "./app-page-actions.mjs";
import { fileURLToPath } from "node:url";
import path from "node:path";
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
process.env.PLAYWRIGHT_BROWSERS_PATH ??= path.join(root, ".pw-browsers");
const { chromium } = await import("playwright");
const port = Number(process.env.REASONIX_SIDEBAR_BROWSER_PORT ?? 4679);
const preview = await startPreviewServer(root, port);
let browser;
try {
  browser = await chromium.launch({ headless: true });
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 }, locale: "en-US" });
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  await page.addInitScript(() => {
    const migrationKey = "projectTree:workbenchSort:createdDefault:v1";
    if (localStorage.getItem(migrationKey) === null) localStorage.setItem("projectTree:workbenchSort", "updated");
  });
  await page.goto(`http://127.0.0.1:${port}/?mock=bench&bench=1`);
  await page.locator(".project-tree__topic-main").first().waitFor();
  assert.equal(await page.locator(".app--workbench").count(), 1);
  assert.equal(await page.locator(".workspace-browser,.app--creation").count(), 0);
  await page.getByRole("button", { name: "More actions", exact: true }).click();
  let sortMenu = page.getByRole("menu", { name: "More actions", exact: true });
  assert.equal(await sortMenu.getByRole("menuitem", { name: "Created time", exact: true }).locator(".context-menu__check").count(), 1,
    "upgrade resets a saved updated-time choice to creation time");
  await sortMenu.getByRole("menuitem", { name: "Updated time", exact: true }).click();
  await page.waitForFunction(() => localStorage.getItem("projectTree:workbenchSort") === "updated");
  await page.reload();
  await page.locator(".project-tree__topic-main").first().waitFor();
  await page.getByRole("button", { name: "More actions", exact: true }).click();
  sortMenu = page.getByRole("menu", { name: "More actions", exact: true });
  assert.equal(await sortMenu.getByRole("menuitem", { name: "Updated time", exact: true }).locator(".context-menu__check").count(), 1,
    "a post-migration manual sort choice survives reload");
  await page.keyboard.press("Escape");
  const actionColumns = await page.evaluate(() => {
    const left = element => element.getBoundingClientRect().left;
    const headerButtons = [...document.querySelectorAll('.sidebar--workbench .project-tree__header-icon-btn')];
    const firstProject = document.querySelector('.sidebar--workbench .project-tree__folder--project');
    const folderButtons = firstProject ? [...firstProject.querySelectorAll('.project-tree__folder-action')] : [];
    return {
      header: headerButtons.map(left),
      folder: folderButtons.map(left),
    };
  });
  assert.equal(actionColumns.header.length, 3, 'project header renders three action columns');
  assert.equal(actionColumns.folder.length, 2, 'expanded project renders menu and create action columns');
  assert.ok(Math.abs(actionColumns.header[1] - actionColumns.folder[0]) < 0.5, `header menu aligns with project menu column: ${JSON.stringify(actionColumns)}`);
  assert.ok(Math.abs(actionColumns.header[2] - actionColumns.folder[1]) < 0.5, `header add action aligns with project create column: ${JSON.stringify(actionColumns)}`);
  assert.equal(await page.locator('.project-tree__topic-main').count(), 5, 'project starts with five rendered sessions');
  await page.getByRole('button', { name: 'Show more in reasonix', exact: true }).click();
  await page.waitForFunction(() => document.querySelectorAll('.project-tree__topic-main').length === 9);
  assert.equal(await page.getByRole('button', { name: 'Show more in reasonix', exact: true }).count(), 0, 'the disclosure disappears at the final page');
  const projectFolder = page.locator('.project-tree__folder--project .project-tree__folder-main').first();
  await projectFolder.click();
  await page.waitForFunction(() => document.querySelector('.project-tree__folder--project .project-tree__folder-main')?.getAttribute('aria-expanded') === 'false');
  await projectFolder.click();
  await page.waitForFunction(() => document.querySelectorAll('.project-tree__topic-main').length === 5);
  assert.equal(await page.getByRole('button', { name: 'Show more in reasonix', exact: true }).count(), 1, 'reopening a project restores the compact window over loaded rows');
  await page.getByRole('button', { name: 'Show more in reasonix', exact: true }).click();
  await page.waitForFunction(() => document.querySelectorAll('.project-tree__topic-main').length === 9);
  await page.getByRole('button', { name: 'Collapse all', exact: true }).click();
  await page.waitForFunction(() => [...document.querySelectorAll('.project-tree__folder-main')].every((element) => element.getAttribute('aria-expanded') === 'false'));
  await page.getByRole('button', { name: 'Restore previous groups', exact: true }).click();
  await page.waitForFunction(() => document.querySelectorAll('.project-tree__topic-main').length === 5);
  assert.equal(await page.getByRole('button', { name: 'Show more in reasonix', exact: true }).count(), 1, 'restoring after collapse all resets the compact window over loaded rows');
  await page.getByRole('button', { name: 'Show more in reasonix', exact: true }).click();
  await page.waitForFunction(() => document.querySelectorAll('.project-tree__topic-main').length === 9);
  await selectSession(page, "bench:small-6t");
  await page.waitForFunction(() => document.querySelector('.transcript')?.textContent?.includes('ASYNC LAYOUT EXPANSION COMPLETE'));
  assert.ok((await page.locator('.topicbar h1').textContent()).includes('bench:small-6t'));
  const geometry = await page.locator('.project-tree__topic-label').first().evaluate(el => ({ whiteSpace: getComputedStyle(el).whiteSpace, overflow: getComputedStyle(el).textOverflow }));
  assert.deepEqual(geometry, { whiteSpace: 'nowrap', overflow: 'ellipsis' });
  await page.getByRole('button', { name: 'Trash', exact: true }).click();
  await page.locator('.archived-sessions:visible').waitFor();
  await page.getByRole('button', { name: 'Back to workspace', exact: true }).click();
  await page.locator('.sidebar__utility-button').filter({ hasText: 'Settings' }).click();
  await page.locator('.settings-page--general').waitFor();
  assert.equal(await page.getByText('Desktop style', { exact: true }).count(), 0);
  assert.deepEqual(errors, []);
  console.log('PASS workbench ProjectTree 5-row disclosure, project and global compact reset, session navigation, archived recovery and settings');
} finally {
  await browser?.close();
  await preview.close();
}
