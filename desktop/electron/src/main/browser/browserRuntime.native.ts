import assert from "node:assert/strict";
import { app, BrowserWindow, nativeImage, webContents } from "electron";
import { createServer } from "node:http";
import { mkdtemp, rm, writeFile, copyFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { ElectronGuestViewFactory } from "./electronGuestViews.js";
import { DocumentRegistry } from "./documents.js";
import { takeSnapshot } from "./snapshot.js";
import { captureScreenshot } from "./screenshot.js";
import type { Logger } from "../log.js";
import { BrowserSurfaceManager } from "./surfaceManager.js";
import { GrantRegistry } from "./grants.js";
import { BrowserRecorder } from "./recorder.js";
import { locateRef, resolveRef } from "./refResolver.js";
import { uploadFiles } from "./upload.js";
import { BrowserDiagnosticExport } from "./diagnosticExport.js";
import { withBrowserDiagnosticRequest } from "./diagnosticContext.js";

app.on("window-all-closed", () => {});
app.on("before-quit", () => console.log("native browser: quitting"));
await app.whenReady();
const server = createServer((req, res) => {
  if (req.url === "/child") { res.end('<button aria-label="Child action" onclick="window.clicked=true">Child action</button>'); return; }
  res.end('<html><body><button aria-label="Save">Save</button><input value="retained"><script>window.identity="same-page"</script></body></html>');
});
await new Promise<void>(resolve => server.listen(0, "127.0.0.1", resolve));
const address = server.address();
assert.ok(address && typeof address === "object");
const directory = await mkdtemp(join(tmpdir(), "reasonix-native-browser-"));
const win = new BrowserWindow({ show: false, width: 800, height: 600 });
const background = process.platform === "darwin";
const recordingSeconds = Number(process.env.REASONIX_BROWSER_RECORD_SECONDS ?? 1);
assert.ok(Number.isInteger(recordingSeconds) && recordingSeconds >= 1 && recordingSeconds <= 90);
if (!background) win.showInactive();
const factory = new ElectronGuestViewFactory({ window: () => win, preloadPath: join(__dirname, "guest-preload.cjs"), log: { warn: console.warn } as Logger });
const protocolTrace: unknown[] = [];
if (process.env.REASONIX_BROWSER_TRACE_RPC === "1") {
  const create = factory.create.bind(factory);
  let request = 0;
  factory.create = (...args) => {
    const guest = create(...args), send = guest.page.debugger.sendCommand.bind(guest.page.debugger);
    const pageId = guest.page.id;
    guest.page.debugger.on("message", (_event, method, params, sessionId?: string) => {
      if (!["Target.attachedToTarget", "Target.detachedFromTarget", "Inspector.detached", "Page.frameAttached", "Page.frameDetached", "Page.frameNavigated", "Runtime.executionContextsCleared"].includes(method)) return;
      const data = params as { sessionId?: string; frameId?: string; reason?: string; waitingForDebugger?: boolean; frame?: { id?: string } };
      protocolTrace.push({ at: performance.now(), pageId, event: method, sessionId, childSession: data.sessionId, frameId: data.frameId ?? data.frame?.id, reason: data.reason, waitingForDebugger: data.waitingForDebugger });
    });
    guest.page.debugger.sendCommand = async (method, params, sessionId) => {
      const id = ++request, started = performance.now();
      const target = sessionId ? "child" : "root";
      protocolTrace.push({ at: started, id, pageId, method, target, phase: "start" });
      try { return await send(method, params, sessionId); }
      finally { protocolTrace.push({ at: performance.now(), id, pageId, method, target, phase: "end", milliseconds: Math.round(performance.now() - started) }); }
    };
    return guest;
  };
}
const view = factory.create("native-browser-test");
let releaseInitialObservation: (() => void) | undefined;
let targetAttachments = 0;
const originalSend = view.page.debugger.sendCommand.bind(view.page.debugger);
view.page.debugger.sendCommand = async (method, params, sessionId) => {
  if (method === "Target.attachToTarget") targetAttachments++;
  return originalSend(method, params, sessionId);
};
// page_not_ready is refused before dispatch and asks the caller to observe
// again: a frame load or zoom change is not a rendered-frame boundary.
let inputRefusals = 0;
const clickObserved = async <T extends { point: { x: number; y: number } }>(observe: (attempt: number) => Promise<T>): Promise<T> => {
  const deadline = Date.now() + 5000;
  for (let attempt = 0; ; attempt++) {
    const observed = await observe(attempt);
    try { await view.sendMouseInput!({ type: "mouseMove", ...observed.point }); }
    catch (error) {
      if ((error as { data?: { kind?: unknown } } | null)?.data?.kind !== "page_not_ready" || Date.now() >= deadline) throw error;
      inputRefusals++;
      continue;
    }
    await view.sendMouseInput!({ type: "mouseDown", button: "left", clickCount: 1, ...observed.point });
    await view.sendMouseInput!({ type: "mouseUp", button: "left", clickCount: 1, ...observed.point });
    return observed;
  }
};
try {
  view.setBounds({ x: 0, y: 0, width: 800, height: 600 });
  view.setVisible(!background);
  await view.page.loadURL(`http://127.0.0.1:${address.port}`);
  const id = view.page.id;
  // Match the host's observation lease before reading a hidden macOS page.
  // Keep the observation lease through input, as hostCalls does. iframe load
  // completion is not a painted-frame boundary, especially for the OOPIF.
  releaseInitialObservation = view.prepareObservation?.();
  const snapshot = await takeSnapshot(view.page, "test", 1, "", new DocumentRegistry());
  assert.match(snapshot.tree, /Save/);
  await view.page.mainFrame.executeJavaScript(`Promise.all(["http://127.0.0.1:${address.port}/child", "http://localhost:${address.port}/child"].map(url => new Promise(resolve => { const frame = document.createElement("iframe"); frame.onload = resolve; frame.src = url; document.body.append(frame); }))).then(() => true)`);
  const frameLease = await view.prepareCapture!(new AbortController().signal);
  const frameDocuments = new DocumentRegistry();
  for (const frame of view.page.mainFrame.framesInSubtree) await frame.executeJavaScript(`window.fixtureEvents = []; for (const type of ['mousedown', 'mouseup', 'click']) document.addEventListener(type, e => window.fixtureEvents.push({type, x:e.clientX, y:e.clientY, tag:e.target.tagName}));`);
  // Holds the page moving until the first input is refused, as a slow runner
  // leaves it unsettled after iframe load; the retry must then land on the target.
  const unsettled = process.env.REASONIX_BROWSER_UNSETTLED_INPUT === "1";
  if (unsettled) await view.page.mainFrame.executeJavaScript(`(() => { let offset = 0; window.fixtureUnsettled = setInterval(() => { document.body.style.paddingTop = \`\${++offset % 40}px\`; }, 4); return true; })()`);
  let observedFrom = 0;
  const { point, located } = await clickObserved(async attempt => {
    if (unsettled && attempt === 1) await view.page.mainFrame.executeJavaScript("clearInterval(window.fixtureUnsettled)");
    observedFrom = targetAttachments;
    const frames = await takeSnapshot(view.page, "test", 1, "", frameDocuments);
    assert.equal((frames.tree.match(/Child action/g) ?? []).length, 2, frames.tree);
    const located = await resolveRef(view.page, frameDocuments.lookup(frames.documentToken)!, "f2e1", true);
    assert.ok(located.ok, JSON.stringify(located));
    return { located: located.value, point: { x: Math.round(located.value.element.x + located.value.element.width / 2), y: Math.round(located.value.element.y + located.value.element.height / 2) } };
  });
  const clickedFrame = located.frame;
  const clickedBy = Date.now() + 1000;
  let clicked = false;
  while (!clicked && Date.now() < clickedBy) { clicked = await clickedFrame.executeJavaScript("Boolean(window.clicked)") as boolean; if (!clicked) await new Promise(resolve => setTimeout(resolve, 16)); }
  if (!clicked) console.error(JSON.stringify({ point, scale: view.inputScale?.(), frames: await Promise.all(view.page.mainFrame.framesInSubtree.map(frame => frame.executeJavaScript(`({events:window.fixtureEvents,width:innerWidth,height:innerHeight,rects:[...document.querySelectorAll('iframe,button')].map(e=>({tag:e.tagName,rect:e.getBoundingClientRect().toJSON()}))})`))) }));
  assert.equal(clicked, true, "cross-origin reference must hit the original element");
  if (unsettled) assert.ok(inputRefusals > 0, "input on a moving page must be refused before dispatch");
  await clickedFrame.executeJavaScript(`(() => { const input = document.createElement("input"); input.type = "file"; input.setAttribute("aria-label", "Attach"); input.onchange = () => { window.uploaded = input.files[0]?.name; }; document.body.append(input); })()`);
  const uploadSnapshot = await takeSnapshot(view.page, "test", 1, "", frameDocuments);
  const uploadRef = uploadSnapshot.tree.match(/"Attach"[^\n]*ref=(f2e\d+)/)?.[1];
  assert.ok(uploadRef, uploadSnapshot.tree);
  const upload = await locateRef(view.page, frameDocuments.lookup(uploadSnapshot.documentToken)!, uploadRef);
  assert.ok(upload.ok, JSON.stringify(upload));
  const uploadPath = join(directory, "fixture.txt");
  await writeFile(uploadPath, "disposable native upload fixture");
  let uploadDispatches = 0;
  assert.equal((await uploadFiles(view.page, upload.value, [uploadPath], () => {}, () => { uploadDispatches++; })).executed, true);
  assert.equal(uploadDispatches, 1);
  assert.equal(await clickedFrame.executeJavaScript("window.uploaded"), "fixture.txt");
  // A refused child-frame attempt discards its session, so count from the observation that clicked.
  assert.ok(targetAttachments - observedFrom <= 1, "snapshot, ref resolution, input and upload must share one OOPIF session");
  const attachedBeforeIdle = targetAttachments;
  releaseInitialObservation?.();
  releaseInitialObservation = undefined;
  console.log("cross-origin file upload through bounded frame runtime: passed");
  await clickedFrame.executeJavaScript(`new Promise(resolve => { const frame = document.createElement('iframe'); frame.style.cssText = 'display:block;width:180px;height:70px;margin:8px'; frame.onload = resolve; frame.src = 'http://localhost:${address.port}/child'; document.body.append(frame); }).then(() => true)`);
  for (const frame of clickedFrame.framesInSubtree) await frame.executeJavaScript(`window.fixtureEvents = []; for (const type of ['mousedown', 'mouseup', 'click']) document.addEventListener(type, e => window.fixtureEvents.push({type, x:e.clientX, y:e.clientY, tag:e.target.tagName}));`);
  const idleBy = Date.now() + 4000;
  while (view.page.debugger.isAttached() && Date.now() < idleBy) await new Promise(resolve => setTimeout(resolve, 20));
  assert.equal(view.page.debugger.isAttached(), false, "idle connection must be released");
  const afterIdle = await takeSnapshot(view.page, "test", 1, "", new DocumentRegistry());
  assert.equal((afterIdle.tree.match(/Child action/g) ?? []).length, 3, afterIdle.tree);
  assert.equal(targetAttachments, attachedBeforeIdle + 1, "idle release must initialize one fresh OOPIF session");
  console.log("CDP idle release and new observation: passed");
  frameLease();
  console.log("same-origin and cross-origin isolated snapshots: passed");
  assert.ok(view.prepareCapture);
  const release = await view.prepareCapture(new AbortController().signal);
  try {
    const shot = await captureScreenshot(view.page, null, 1, { ref: "", fullPage: false, directory }, { decodePNG: data => nativeImage.createFromBuffer(data).getSize() });
    assert.ok(shot.width >= 800 && shot.height >= 600);
    await copyFile(shot.path, join(__dirname, "browser-evidence.png"));
    if (background) assert.equal(BrowserWindow.getFocusedWindow(), null);
    console.log(JSON.stringify({ platform: process.platform, electron: process.versions.electron, screenshot: { width: shot.width, height: shot.height }, snapshot: "passed" }));
  } finally { release(); }
  assert.equal(view.page.id, id);
  assert.equal(await view.page.mainFrame.executeJavaScript('window.identity + ":" + document.querySelector("input").value'), "same-page:retained");
  assert.equal(win.isVisible(), !background);
  assert.ok(view.setViewport);
  // Exercise foreground Fit input separately from capture (which temporarily
  // uses scale 1). Hit both same-process and out-of-process child frames.
  win.showInactive();
  view.setVisible(true);
  const releaseFitObservation = view.prepareObservation?.();
  try {
    for (let transition = 0; transition < 3; transition++) {
      view.setViewport(null);
      view.page.setZoomFactor(1.25);
      const { zoomPoint, point: zoomedPoint } = await clickObserved(async () => {
        const zoomPoint = await view.page.mainFrame.executeJavaScript(`(() => { window.fixtureZoomClicked = false; const button = document.querySelector('button'); button.onclick = () => { window.fixtureZoomClicked = true; }; button.scrollIntoView(); const r = button.getBoundingClientRect(); return {x:r.x+r.width/2,y:r.y+r.height/2}; })()`) as { x: number; y: number };
        return { zoomPoint, point: { x: Math.round(zoomPoint.x * 1.25), y: Math.round(zoomPoint.y * 1.25) } };
      });
      let zoomClicked = false;
      const zoomDeadline = Date.now() + 1000;
      while (!zoomClicked && Date.now() < zoomDeadline) {
        zoomClicked = await view.page.mainFrame.executeJavaScript("Boolean(window.fixtureZoomClicked)") as boolean;
        if (!zoomClicked) await new Promise(resolve => setTimeout(resolve, 16));
      }
      if (!zoomClicked) console.error(JSON.stringify({ zoomPoint, zoomedPoint, frames: await Promise.all(view.page.mainFrame.framesInSubtree.map(frame => frame.executeJavaScript("({width:innerWidth,events:window.fixtureEvents,rect:document.querySelector('button').getBoundingClientRect().toJSON()})"))) }));
      assert.equal(zoomClicked, true, "browser zoom must be converted exactly once");
      console.log("natural viewport page zoom input: passed");
      for (const displayScale of [null, "fit", 0.5, 0.75] as const) {
        if (displayScale !== null) view.setViewport({ width: 1280, height: 720, scale: displayScale });
        for (const prefix of ["f1", "f2", "f3"]) {
          const { target, scale, point } = await clickObserved(async () => {
            const docs = new DocumentRegistry();
            const observed = await takeSnapshot(view.page, "test", 1, "", docs);
            const ref = observed.tree.match(new RegExp(`"Child action"[^\\n]*ref=(${prefix}e\\d+)`))?.[1];
            assert.ok(ref, observed.tree);
            const resolved = await resolveRef(view.page, docs.lookup(observed.documentToken)!, ref, true);
            assert.ok(resolved.ok, JSON.stringify(resolved));
            await resolved.value.frame.executeJavaScript("window.clicked = false");
            const scale = view.inputScale?.() ?? 1;
            return { target: resolved.value, scale, point: { x: Math.round((resolved.value.element.x + resolved.value.element.width / 2) * scale), y: Math.round((resolved.value.element.y + resolved.value.element.height / 2) * scale) } };
          });
          const clickDeadline = Date.now() + 1000;
          let fitClicked = false;
          while (!fitClicked && Date.now() < clickDeadline) {
            fitClicked = await target.frame.executeJavaScript("Boolean(window.clicked)") as boolean;
            if (!fitClicked) await new Promise(resolve => setTimeout(resolve, 16));
          }
          if (!fitClicked) console.error(JSON.stringify({ prefix, point, scale, element: target.element, frames: await Promise.all(view.page.mainFrame.framesInSubtree.map(frame => frame.executeJavaScript(`({events:window.fixtureEvents,width:innerWidth,height:innerHeight,rects:[...document.querySelectorAll('iframe,button')].map(e=>({tag:e.tagName,rect:e.getBoundingClientRect().toJSON()}))})`))) }));
          assert.equal(fitClicked, true, `foreground Fit must hit ${prefix}, scale=${scale}, point=${JSON.stringify(point)}`);
          const receipt = await target.frame.executeJavaScript(`(() => { const event = window.fixtureEvents.filter(e => e.type === 'click').at(-1); const rect = document.querySelector('button').getBoundingClientRect(); return {event, x:rect.x+rect.width/2, y:rect.y+rect.height/2}; })()`) as { event: { x: number; y: number }; x: number; y: number };
          assert.ok(Math.abs(receipt.event.x - receipt.x) <= 2 && Math.abs(receipt.event.y - receipt.y) <= 2, `pointer must hit the observed centre, not merely the same large button: ${JSON.stringify({ prefix, scale, receipt })}`);
        }
      }
    }
  } finally { releaseFitObservation?.(); }
  console.log("foreground Fit same-origin and cross-origin clicks: passed");
  view.setVisible(!background);
  if (background) win.hide();
  view.setViewport({ width: 393, height: 852, scale: "fit" });
  const responsive = await view.prepareCapture(new AbortController().signal);
  try {
    const viewport = await view.page.mainFrame.executeJavaScript('({width: innerWidth, height: innerHeight, dpr: devicePixelRatio})') as { width: number; height: number; dpr: number };
    assert.deepEqual(viewport, { width: 393, height: 852, dpr: 1 });
    const shot = await captureScreenshot(view.page, null, view.inputScale?.() ?? 1, { ref: "", fullPage: false, directory }, { viewport: { width: 393, height: 852 }, pixelRatio: view.capturePixelRatio?.(), decodePNG: data => nativeImage.createFromBuffer(data).getSize() });
    assert.equal(shot.width, 393); assert.equal(shot.height, 852);
    console.log("responsive CSS viewport and PNG: passed");
  } finally { responsive(); }
  view.destroy();
  const surfaces = new BrowserSurfaceManager({ views: factory, contentSize: () => ({ width: 800, height: 600 }), onTakeover() {}, onCrash() {}, log: { warn: console.warn } as Logger });
  surfaces.setLayout({ x: 0, y: 0, width: 800, height: 600 });
  const tab = await surfaces.open(`http://127.0.0.1:${address.port}`, { taskId: "native-task", sessionId: "native-session", temporary: true });
  if (!background) surfaces.activate(tab.id);
  const grants = new GrantRegistry({ generation: () => "native-generation" });
  const diagnosticScope = "a".repeat(64);
  const diagnosticExport = new BrowserDiagnosticExport(surfaces, grants, { build: "native-production-bundle", version: process.versions.electron, platform: process.platform });
  diagnosticExport.bind(grants.install({ grantId: "native-grant", taskId: tab.taskId, sessionId: tab.sessionId, diagnosticScope }));
  await tab.view.page.mainFrame.executeJavaScript('console.error("HTTPS://user:NATIVE-SECRET@example.test/path?code=NATIVE-SECRET#NATIVE-SECRET HtTp://example.test/path?signature=NATIVE-SECRET")');
  await tab.view.page.mainFrame.executeJavaScript(`new Promise(resolve => { const frame = document.createElement("iframe"); frame.onload = resolve; frame.src = "http://localhost:${address.port}/child"; document.body.append(frame); }).then(() => true)`);
  // Match host/browser.snapshot's observation lease. Calling the DOM runtime
  // directly on an unleased hidden tab exercises Chromium's throttled state,
  // not the tool path being qualified.
  const releaseDiagnosticObservation = tab.view.prepareObservation?.();
  try {
    const releaseDiagnosticSurface = await tab.view.prepareCapture!(new AbortController().signal);
    try {
      const result = await withBrowserDiagnosticRequest({ requestId: "native-request", operationId: "native-operation", method: "host/browser.snapshot", tabId: tab.id }, event => diagnosticExport.trace(diagnosticScope, event), () => takeSnapshot(tab.view.page, tab.id, tab.epoch, "", new DocumentRegistry()));
      assert.match(result.tree, /Child action/, "diagnostic fixture must actually observe its cross-origin child");
    } finally { releaseDiagnosticSurface(); }
  } finally { releaseDiagnosticObservation?.(); }
  const recorder = new BrowserRecorder(surfaces, grants, __dirname);
  try {
    const recording = await recorder.request(tab, "native-grant", "start", "", directory, recordingSeconds);
    const until = Date.now() + recordingSeconds * 1000 + 15_000;
    let result = recording;
    while (!["completed", "interrupted", "cancelled", "failed"].includes(result.state) && Date.now() < until) {
      await new Promise(resolve => setTimeout(resolve, 100));
      result = await recorder.request(tab, "native-grant", "status", recording.id, directory);
    }
    assert.equal(result.state, "completed", JSON.stringify(result));
    assert.ok(result.bytes > 32 && result.width && result.height);
    assert.ok(result.durationMs! >= recordingSeconds * 1000 - 500 && result.durationMs! <= recordingSeconds * 1000 + 10_000, JSON.stringify(result));
    await copyFile(result.path!, join(__dirname, "browser-recording.webm"));
    console.log(JSON.stringify({ recording: result.state, bytes: result.bytes, width: result.width, height: result.height, durationMs: result.durationMs }));
    const interrupted = await recorder.request(tab, "native-grant", "start", "", directory, 20);
    surfaces.takeover(tab.id, "native takeover test");
    await recorder.close();
    const stopped = await recorder.request(tab, "native-grant", "status", interrupted.id, directory);
    assert.equal(stopped.state, "interrupted"); assert.equal(stopped.path, undefined);
    console.log("recording takeover: interrupted without publishing an artifact");
    assert.equal((await recorder.userRequest(tab, "start", directory))?.state, "recording");
    assert.equal(surfaces.takeoverFromSender(tab.view.page.id, "keydown"), true);
    assert.equal((await recorder.userRequest(tab, "status", directory))?.state, "recording");
    const userRecording = await recorder.userRequest(tab, "stop", directory);
    assert.equal(userRecording?.state, "completed", JSON.stringify(userRecording));
    assert.ok(userRecording.bytes > 32 && userRecording.path);
    console.log("user recording: input preserved recording and stop published a WebM");
  } finally { await recorder.close(); surfaces.destroyAll(); console.log("native browser: recording resources released"); }
  const diagnosticResult = diagnosticExport.read(diagnosticScope);
  assert.ok(diagnosticResult.entries.some(row => row.kind === "page"), "native console event must reach the session export");
  assert.ok(diagnosticResult.entries.some(row => row.command === "Page.createIsolatedWorld" && row.requestId === "native-request"), "isolated-world command must retain its diagnostic request attribution");
  assert.ok(diagnosticResult.entries.some(row => row.event === "tab_closed"), "closed tab must remain in the export");
  assert.equal(JSON.stringify(diagnosticResult).includes("NATIVE-SECRET"), false);
  assert.equal(diagnosticResult.tabs.length, 0);
  await writeFile(join(__dirname, "browser-diagnostics.json"), JSON.stringify(diagnosticResult, null, 2));
  diagnosticExport.dispose();
  console.log("browser diagnostics: native page errors, CDP attribution, closed-tab retention and redaction passed");
  const cycles = Number(process.env.REASONIX_BROWSER_CYCLES ?? 1);
  assert.ok(Number.isInteger(cycles) && cycles >= 1 && cycles <= 100);
  for (let cycle = 0; cycle < cycles; cycle++) {
    const page = factory.create("native-cycle");
    page.setBounds({ x: 0, y: 0, width: 640, height: 480 });
    page.setVisible(!background);
    try {
      await page.page.loadURL(`http://127.0.0.1:${address.port}/child`);
      await page.page.mainFrame.executeJavaScript(`Promise.all(["http://127.0.0.1:${address.port}/child", "http://localhost:${address.port}/child"].map(url => new Promise(resolve => { const frame = document.createElement("iframe"); frame.onload = resolve; frame.src = url; document.body.append(frame); }))).then(() => true)`);
      const lease = await page.prepareCapture!(new AbortController().signal);
      try {
        const documents = new DocumentRegistry();
        const observed = await takeSnapshot(page.page, "cycle", cycle, "", documents);
        assert.equal((observed.tree.match(/Child action/g) ?? []).length, 3, observed.tree);
        const childRef = await resolveRef(page.page, documents.lookup(observed.documentToken)!, "f2e1", false);
        assert.ok(childRef.ok, JSON.stringify(childRef));
        await captureScreenshot(page.page, null, 1, { ref: "", fullPage: false, directory });
      } finally { lease(); }
    } finally { page.destroy(); }
    const releasedBy = Date.now() + 2000;
    while (webContents.getAllWebContents().length > 1 && Date.now() < releasedBy) await new Promise(resolve => setTimeout(resolve, 10));
    assert.equal(webContents.getAllWebContents().length, 1, `cycle ${cycle + 1}: only owner window may remain`);
  }
  console.log(`native browser: ${cycles} lifecycle cycles released all pages`);
} finally {
  releaseInitialObservation?.();
  view.destroy(); win.destroy(); server.close();
  await rm(directory, { recursive: true, force: true });
  const deadline = Date.now() + 5000;
  while (webContents.getAllWebContents().length && Date.now() < deadline) await new Promise(resolve => setTimeout(resolve, 20));
  assert.equal(webContents.getAllWebContents().length, 0, "all native pages and capture/recorder hosts must be released");
  console.log("native browser: cleanup complete");
  // Buffer protocol traces to avoid per-command terminal IO perturbing races.
  if (protocolTrace.length) console.log(protocolTrace.map(row => JSON.stringify(row)).join("\n"));
}
await writeFile(join(__dirname, "result.json"), JSON.stringify({ completed: true, platform: process.platform, electron: process.versions.electron, chrome: process.versions.chrome, cycles: Number(process.env.REASONIX_BROWSER_CYCLES ?? 1), backgroundCapture: background, isolatedCrossOriginSnapshot: true, crossOriginClick: true, crossOriginUpload: true, crossOriginLifecycleCycles: true, idleReconnect: true, responsive: { width: 393, height: 852, dpr: 1 }, recording: { mime: "video/webm", width: 1280, height: 720, requestedSeconds: recordingSeconds, takeover: "interrupted", userInput: "continued", userStop: "completed" }, remainingWebContents: 0 }));
