import assert from "node:assert/strict";
import { test } from "node:test";
import { StartupDelay, renderStartupPage } from "./startupDelay.js";
import { startupPresentation } from "./startupPresentation.js";

function fixture() {
  const callbacks: (() => void)[] = [];
  let shown = 0;
  const delay = new StartupDelay((callback) => { callbacks.push(callback); return () => {}; });
  return { delay, callbacks, show: () => shown++, shown: () => shown };
}

test("slow startup displays once; repeated clicks do not restart its deadline", () => {
  const f = fixture();
  f.delay.start(f.show);
  assert.equal(f.shown(), 0);
  for (let i = 0; i < 3; i++) assert.equal(startupPresentation({ serviceReady: false, hasWindow: false, lifecycle: "starting" }), "none");
  f.callbacks[0]();
  f.callbacks[0]();
  assert.equal(f.shown(), 1);
  assert.equal(startupPresentation({ serviceReady: false, hasWindow: true, lifecycle: "starting" }), "focus");
});

for (const outcome of ["ready", "failed", "quit"]) {
  test(`${outcome} cancels an already queued startup callback`, () => {
    const f = fixture();
    f.delay.start(f.show);
    f.delay.cancel();
    f.callbacks[0]();
    assert.equal(f.shown(), 0);
  });
}

test("a previous startup cannot display over a newer generation", () => {
  const f = fixture();
  f.delay.start(f.show);
  f.delay.start(f.show);
  f.callbacks[0]();
  assert.equal(f.shown(), 0);
  f.callbacks[1]();
  assert.equal(f.shown(), 1);
});

test("startup page contains no diagnostic actions or executable script", () => {
  const html = renderStartupPage();
  assert.match(html, /正在启动/);
  assert.doesNotMatch(html, /<script|href=|__shell|Logs:/);
  assert.match(html, /default-src 'none'/);
});
