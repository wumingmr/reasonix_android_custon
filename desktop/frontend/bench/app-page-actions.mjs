/** The desktop ships one workbench layout; this waits for its chrome. */
export async function chooseAppLayout() {
  return undefined;
}

/**
 * Locate a local session through the workbench's ProjectTree navigation.
 */
export function sessionButton(page, label) {
  return page.locator(".project-tree__topic-main").filter({ hasText: label }).first();
}

export async function revealSession(page, label, projectLabel = "reasonix") {
  // Composer readiness and sidebar data readiness are independent. Wait for
  // the first projected row before deciding whether the target is truncated.
  await page.locator(".project-tree__topic-main").first().waitFor({ state: "visible" });
  const button = sessionButton(page, label);
  const showMore = page.getByRole("button", { name: `Show more in ${projectLabel}`, exact: true });
  while (await button.count() === 0 || !await button.isVisible()) {
    if (await showMore.count() === 0 || !await showMore.isVisible()) break;
    const renderedCount = await page.locator(".project-tree__topic-main").count();
    await showMore.click();
    await page.waitForFunction(({ targetLabel, previousCount }) => {
      const rows = [...document.querySelectorAll(".project-tree__topic-main")];
      return rows.some((row) => row.textContent?.includes(targetLabel)) || rows.length > previousCount;
    }, { targetLabel: label, previousCount: renderedCount });
  }
  await button.waitFor({ state: "visible" });
  return button;
}

export async function selectSession(page, label) {
  const button = await revealSession(page, label);
  await button.click();
}

/** Return the visible new-session action. */
export function newSessionButton(page) {
  return page.locator(".sidebar__quick-action:visible").first();
}

export function activeSessionLabel(page) {
  return page.locator('.project-tree__topic--active .project-tree__topic-label').first();
}

export async function readActiveSessionLabel(page) {
  const label = activeSessionLabel(page);
  return await label.count() > 0 ? await label.textContent() ?? "" : "";
}
