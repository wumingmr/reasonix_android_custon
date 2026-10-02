import assert from "node:assert/strict";
import { test } from "node:test";
import { JSDOM } from "jsdom";
import { scriptCall } from "./pageScripts.js";

test("production input preparation observes painted geometry and cleans up every exit", async () => {
  for (const change of ["stable", "moved", "replaced", "detached", "unpainted"] as const) {
    const dom = new JSDOM("<button>Target</button><div>Other</div>", { runScripts: "outside-only" });
    const { window } = dom;
    const target = window.document.querySelector("button")!;
    let current: Element = target, x = 10, frameCallback: FrameRequestCallback | undefined, timeoutCallback: (() => void) | undefined;
    window.document.elementFromPoint = () => current as unknown as HTMLElement;
    target.getBoundingClientRect = () => ({ x, y: 20, width: 40, height: 20 }) as DOMRect;
    window.requestAnimationFrame = callback => { frameCallback = callback; return 1; };
    window.cancelAnimationFrame = () => { frameCallback = undefined; };
    window.setTimeout = ((callback: () => void) => { timeoutCallback = callback; return 1; }) as typeof window.setTimeout;
    window.clearTimeout = () => { timeoutCallback = undefined; };
    try {
      const result = window.eval(scriptCall("pageInputReady", { x: 20, y: 25 })) as Promise<boolean>;
      let settled = false;
      void result.then(() => { settled = true; });
      frameCallback!(1);
      await Promise.resolve();
      assert.equal(settled, false, "one layout sample is not a rendered-frame boundary");
      if (change === "moved") x++;
      if (change === "replaced") current = window.document.querySelector("div")!;
      if (change === "detached") target.remove();
      if (change === "unpainted") timeoutCallback!(); else frameCallback!(2);
      assert.equal(await result, change === "stable", change);
      assert.equal(frameCallback, undefined);
      assert.equal(timeoutCallback, undefined);
    } finally { window.close(); }
  }
});
