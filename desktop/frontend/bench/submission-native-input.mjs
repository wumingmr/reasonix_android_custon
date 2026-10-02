import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";

// XTest input targets only the disposable Electron window in an isolated Xvfb
// display. It must never run against the developer's active desktop session.
export async function verifyNativeReaderInput(page, evidence = {}) {
  evidence.status = "running";
  assert.equal(process.platform, "linux");
  assert.ok(process.env.DISPLAY, "native input requires an isolated Xvfb display");
  const xdo = (...args) => execFileSync("xdotool", args.map(String), { encoding: "utf8", timeout: 10_000 }).trim();
  const windows = xdo("search", "--onlyvisible", "--name", "^Reasonix Handoff Native$").split("\n");
  assert.equal(windows.length, 1, "exactly one disposable native window is required");
  const windowId = windows[0];
  xdo("windowfocus", "--sync", windowId);
  const frame = () => page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  const scroll = page.locator(".chat-flow-scroll");
  await scroll.evaluate(el => {
    el.focus();
    window.nativeSamples = [];
    window.nativeSampling = true;
    window.nativePhase = "position";
    window.nativePointerEvents = [];
    for (const type of ["pointerdown", "pointerup", "pointermove"]) el.addEventListener(type, event => {
      window.nativePointerEvents.push({ type, x: event.clientX, y: event.clientY, buttons: event.buttons, target: event.target === el });
    });
    const sample = () => {
      const box = el.getBoundingClientRect();
      const rows = [...el.querySelectorAll("[data-chat-kind]")];
      const visible = rows.some(row => { const rect = row.getBoundingClientRect(); return rect.height > 0 && rect.bottom > box.top && rect.top < box.bottom; });
      window.nativeSamples.push({ phase: window.nativePhase, top: el.scrollTop, height: el.scrollHeight, viewport: el.clientHeight, blank: !visible });
      if (window.nativeSampling) requestAnimationFrame(sample);
    };
    sample();
  });
  const top = () => scroll.evaluate(el => el.scrollTop);
  try {
    xdo("key", "--clearmodifiers", "End");
    await page.waitForFunction(() => { const el = document.querySelector(".chat-flow-scroll"); return el.scrollHeight - el.clientHeight - el.scrollTop <= 1; });
    const box = await scroll.boundingBox();
    assert.ok(box);
    const center = { x: Math.round(box.x + box.width / 2), y: Math.round(box.y + box.height / 2) };
    xdo("mousemove", "--window", windowId, center.x, center.y);
    await page.waitForFunction(() => window.nativePointerEvents.some(event => event.type === "pointermove"));
    const observed = await page.evaluate(() => window.nativePointerEvents.filter(event => event.type === "pointermove").at(-1));
    // Window-manager borders can offset X11 window-relative and DOM client
    // coordinates. Measure that transform with a harmless move over content.
    const offset = { x: center.x - observed.x, y: center.y - observed.y };
    evidence.coordinateOffset = offset;
    const beforeWheel = await top();
    await page.evaluate(() => { window.nativePhase = "wheel"; });
    xdo("click", "--repeat", 4, "--delay", 50, 4);
    await page.waitForFunction(before => document.querySelector(".chat-flow-scroll").scrollTop < before - 50, beforeWheel);
    await frame();
    const afterWheel = await top();
    evidence.wheel = { before: beforeWheel, after: afterWheel };
    await scroll.evaluate(el => el.focus());
    const beforeKeyboard = await top();
    await page.evaluate(() => { window.nativePhase = "keyboard"; });
    xdo("key", "--clearmodifiers", "Page_Up");
    await page.waitForFunction(before => document.querySelector(".chat-flow-scroll").scrollTop < before - 50, beforeKeyboard);
    await frame();
    const afterKeyboard = await top();
    evidence.keyboard = { before: beforeKeyboard, after: afterKeyboard };
    await page.evaluate(() => { window.nativePhase = "position"; });
    xdo("key", "--clearmodifiers", "End");
    await page.waitForFunction(() => { const el = document.querySelector(".chat-flow-scroll"); return el.scrollHeight - el.clientHeight - el.scrollTop <= 1; });
    const track = await scroll.evaluate(el => {
      const box = el.getBoundingClientRect();
      // clientLeft includes the reserved left gutter with stable both-edges.
      const gutter = el.offsetWidth - el.clientWidth - el.clientLeft - parseFloat(getComputedStyle(el).borderRightWidth);
      const thumb = Math.max(gutter, (box.height - 2 * gutter) * el.clientHeight / el.scrollHeight);
      return { x: box.right - gutter / 2, y: box.bottom - gutter - thumb / 2, to: box.top + box.height / 2, gutter, top: el.scrollTop };
    });
    assert.ok(track.gutter > 0, "native scrollbar must be exposed");
    evidence.track = track;
    evidence.phase = "drag-start";
    await page.evaluate(() => { window.nativePhase = "scrollbar"; });
    xdo("mousemove", "--window", windowId, Math.round(track.x + offset.x), Math.round(track.y + offset.y));
    xdo("mousedown", 1);
    try {
      for (let step = 1; step <= 12; step++) {
        xdo("mousemove", "--window", windowId, Math.round(track.x + offset.x), Math.round(track.y + (track.to - track.y) * step / 12 + offset.y));
        // Do not await renderer frames inside a native pointer transaction:
        // the scrollbar's modal drag loop may defer JS until mouse release.
        xdo("sleep", "0.03");
      }
    } finally { xdo("mouseup", 1); }
    evidence.phase = "drag-released";
    await page.waitForFunction(before => document.querySelector(".chat-flow-scroll").scrollTop < before - 100, track.top);
    await frame();
    evidence.phase = "drag-observed";
    assert.equal(await scroll.getAttribute("data-scroll-mode"), "reader");
    const samples = await page.evaluate(() => { window.nativeSampling = false; return window.nativeSamples; });
    const heights = samples.map(sample => sample.height);
    const extent = { initial: heights[0], min: Math.min(...heights), max: Math.max(...heights), final: heights.at(-1) };
    const blankFrames = samples.filter(sample => sample.blank).length;
    const reverseDisplacement = Math.max(0, ...samples.slice(1).map((sample, index) =>
      sample.phase !== "position" && sample.phase === samples[index].phase ? sample.top - samples[index].top : 0));
    assert.equal(blankFrames, 0, "native input must not expose blank transcript frames");
    assert.ok(extent.max - extent.final <= Math.max(96, samples.at(-1).viewport * 0.5), "native input must not collapse the scroll range");
    return Object.assign(evidence, { status: "passed", method: "X11 XTest through xdotool", wheel: { before: beforeWheel, after: afterWheel },
      keyboard: { before: beforeKeyboard, after: afterKeyboard }, scrollbar: { before: track.top, after: await top(), gutter: track.gutter },
      extent, blankFrames, reverseDisplacement, samples });
  } finally {
    Object.assign(evidence, await page.evaluate(() => {
      window.nativeSampling = false;
      return { samples: window.nativeSamples, pointerEvents: window.nativePointerEvents, screen: {
        innerWidth, innerHeight, outerWidth, outerHeight, devicePixelRatio, screenX, screenY,
      } };
    }));
  }
}
