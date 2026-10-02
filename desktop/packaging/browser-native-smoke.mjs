// Exercise packaged Go -> Electron browser tools -> provider image delivery.
// The loopback provider is deterministic; this does not test model perception.
// Usage: node desktop/packaging/browser-native-smoke.mjs <Reasonix.app> <evidence-dir>
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { createRequire } from "node:module";
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { packagedSmokeEnv } from "./smoke-env.mjs";
import { waitForSmokeCondition } from "./smoke-poll.mjs";
import { closeAndVerify } from "./smoke-lifecycle.mjs";

const require = createRequire(new URL("../electron/package.json", import.meta.url));
const { _electron } = require("playwright");
const bundle = resolve(process.argv[2]), evidence = resolve(process.argv[3]);
const home = mkdtempSync(join(tmpdir(), "reasonix-browser-packaged-"));
const project = join(home, "project");
mkdirSync(project); mkdirSync(evidence, { recursive: true });
writeFileSync(join(evidence, "result.json"), JSON.stringify({ completed: false, status: "running" }));
let application, page, step = 0, failure, receivedImage = false, tabId, token, ref, browserPageId;
const steps = [];
function images(value, found = []) {
  if (!value || typeof value !== "object") return found;
  if (value.type === "image_url") found.push(value.image_url.url);
  for (const child of Object.values(value)) if (child && typeof child === "object") images(child, found);
  return found;
}
function textOf(message) {
  return typeof message?.content === "string" ? message.content : (message?.content ?? []).filter(part => part.type === "text").map(part => part.text).join("\n");
}
const server = createServer(async (req, res) => {
  if (req.method === "GET") {
    res.setHeader("Content-Type", "text/html; charset=utf-8");
    if (req.url === "/child") { res.end('<button onclick="this.textContent=\'Frame verified\'">Frame action</button>'); return; }
    res.end(`<h1>Browser acceptance</h1><input value="retained"><iframe src="http://localhost:${server.address().port}/child"></iframe>`);
    return;
  }
  try {
    let raw = "";
    for await (const chunk of req) raw += chunk;
    const request = JSON.parse(raw);
    let delta = { content: "Browser fixture" }, finish = "stop";
    if (request.tools?.some(tool => tool.function?.name === "use_capability")) {
      const result = textOf([...request.messages].reverse().find(message => message.role === "tool"));
      let capability, args;
      if (step === 0) { capability = "browser_open"; args = { operationId: "open-fixture", url: `http://127.0.0.1:${server.address().port}/fixture`, temporary: true }; }
      if (step === 1) {
        tabId = result.match(/opened tab ([^: ]+):/)?.[1]; assert.ok(tabId, result);
        capability = "browser_snapshot"; args = { tabId };
      }
      if (step === 2) {
        token = result.match(/documentToken: (\S+)/)?.[1];
        ref = result.match(/button "Frame action"[^\n]*ref=(f\d+e\d+)/)?.[1];
        assert.ok(token && ref, result);
        browserPageId = await application.evaluate(async ({ webContents }, url) => {
          const guest = webContents.getAllWebContents().find(page => page.getURL() === url);
          const send = guest.debugger.sendCommand.bind(guest.debugger);
          guest.fixtureInputs = [];
          guest.debugger.sendCommand = async (method, params, session) => {
            if (method === "Input.dispatchMouseEvent") guest.fixtureInputs.push(params);
            return send(method, params, session);
          };
          for (const frame of guest.mainFrame.framesInSubtree) await frame.executeJavaScript(`window.fixtureEvents = []; for (const type of ['mousedown', 'mouseup', 'click']) document.addEventListener(type, e => window.fixtureEvents.push({type, x:e.clientX, y:e.clientY, tag:e.target.tagName}));`);
          return guest.id;
        }, `http://127.0.0.1:${server.address().port}/fixture`);
        capability = "browser_click"; args = { tabId, documentToken: token, operationId: "click-fixture", ref };
      }
      if (step === 3) { assert.match(result, /executed|clicked|ok/i); capability = "browser_snapshot"; args = { tabId }; }
      if (step === 4) {
        const inputEvidence = await application.evaluate(async ({ webContents }, id) => {
          const guest = webContents.fromId(id);
          return { inputs: guest.fixtureInputs, frames: await Promise.all(guest.mainFrame.framesInSubtree.map(frame => frame.executeJavaScript(`({events:window.fixtureEvents,width:innerWidth,height:innerHeight,rects:[...document.querySelectorAll('iframe,button')].map(e=>({tag:e.tagName,rect:e.getBoundingClientRect().toJSON()}))})`))) };
        }, browserPageId);
        writeFileSync(join(evidence, "input-evidence.json"), JSON.stringify(inputEvidence, null, 2));
        assert.match(result, /Frame verified/);
        browserPageId = await application.evaluate(({ webContents }, url) => webContents.getAllWebContents().find(page => page.getURL() === url)?.id,
          `http://127.0.0.1:${server.address().port}/fixture`);
        assert.ok(browserPageId);
        await page.locator(".workbench-dock__collapse").click();
        await page.locator(".browser-panel").waitFor({ state: "detached" });
        capability = "browser_screenshot"; args = { tabId };
      }
      if (step === 5) {
        const data = images(request).find(url => url.startsWith("data:image/png;base64,"));
        assert.ok(data, "provider received no structured screenshot image");
        const bytes = Buffer.from(data.split(",")[1], "base64");
        assert.equal(bytes.subarray(0, 8).toString("hex"), "89504e470d0a1a0a");
        assert.ok(bytes.readUInt32BE(16) > 0 && bytes.readUInt32BE(20) > 0);
        writeFileSync(join(evidence, "provider-browser.png"), bytes);
        receivedImage = true;
        delta = { content: "BROWSER_ACCEPTANCE_DONE" };
      }
      assert.ok(step <= 5, "unexpected extra provider round trip");
      if (capability) {
        steps.push(capability);
        delta = { tool_calls: [{ index: 0, id: `browser-fixture-${step}`, type: "function", function: { name: "use_capability", arguments: JSON.stringify({ action: "call", capability_id: `tool:${capability}`, arguments: args }) } }] };
        finish = "tool_calls";
      }
      step++;
    }
    res.writeHead(200, { "Content-Type": "text/event-stream" });
    res.end(`data: ${JSON.stringify({ id: "browser-fixture", choices: [{ index: 0, delta, finish_reason: null }] })}\n\n`
      + `data: ${JSON.stringify({ id: "browser-fixture", choices: [{ index: 0, delta: {}, finish_reason: finish }] })}\n\ndata: [DONE]\n\n`);
  } catch (error) { failure = error; res.writeHead(500); res.end("fixture assertion failed"); }
});
await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
writeFileSync(join(home, "config.toml"), `default_model = "fixture/vision"\n[desktop]\nprovider_access = ["fixture"]\n[[providers]]\nname = "fixture"\nkind = "openai"\nbase_url = "http://127.0.0.1:${server.address().port}/v1"\nmodels = ["vision"]\nvision_models = ["vision"]\ndefault = "vision"\napi_key_env = "BROWSER_FIXTURE_KEY"\n`);
try {
  application = await _electron.launch({ executablePath: join(bundle, "Contents/MacOS/Reasonix"), env: { ...packagedSmokeEnv(process.env, home), BROWSER_FIXTURE_KEY: "loopback-only" }, timeout: 60_000 });
  const manifest = JSON.parse(readFileSync(join(bundle, "Contents/Resources/build.json"), "utf8"));
  await waitForSmokeCondition(async () => {
    for (const candidate of application.windows()) {
      try { if (await candidate.evaluate(() => window.reasonixDesktop?.invoke("Version", [])) === manifest.version) { page = candidate; return true; } } catch { /* Splash replacement. */ }
    }
    return false;
  }, { timeout: 60_000 });
  assert.equal(await application.evaluate(({ app }) => app.isPackaged), true);
  const invoke = (method, args = []) => page.evaluate(({ method, args }) => window.reasonixDesktop.invoke(method, args), { method, args });
  const tab = await invoke("EnsureBlankSurface", ["project", project]);
  await invoke("SetActiveTab", [tab.id]);
  await invoke("SetToolApprovalModeForTab", [tab.id, "yolo"]);
  await invoke("StartTurnForTab", [tab.id, "BROWSER_FIXTURE: inspect the deterministic local page", "browser-native-fixture"]);
  await waitForSmokeCondition(() => { if (failure) throw failure; return receivedImage; }, { timeout: 60_000 });
  await waitForSmokeCondition(async () => (await invoke("ListTabs")).every(tab => !tab.running));
  assert.equal(await page.locator(".browser-panel").count(), 0, "background screenshot must not reopen the panel");
  const retained = await application.evaluate(({ webContents }, id) => webContents.fromId(id)?.executeJavaScript('document.querySelector("input").value'), browserPageId);
  assert.equal(retained, "retained", "capture must retain the original page and form");
  const decoded = await application.evaluate(({ nativeImage }, path) => {
    const image = nativeImage.createFromPath(path);
    return { empty: image.isEmpty(), ...image.getSize(), colors: new Set(image.toBitmap()).size };
  }, join(evidence, "provider-browser.png"));
  assert.equal(decoded.empty, false); assert.ok(decoded.colors > 2, "screenshot must contain rendered content");
  const shellPid = await application.evaluate(() => process.pid);
  const log = readFileSync(join(home, "desktop-shell/logs/shell.log"), "utf8");
  const servicePid = Number(log.match(/desktop service ready: generation \S+, pid (\d+)/)?.[1]);
  assert.ok(servicePid);
  await closeAndVerify(application, { shellPid, servicePid }); application = null;
  writeFileSync(join(evidence, "result.json"), JSON.stringify({ completed: true, version: manifest.version, steps, providerReceivedPNG: true, backgroundCapturePreservesPage: true, closedPanelStaysClosed: true, decoded, provider: "loopback fixture; no model perception claim", normalExit: true }, null, 2));
  console.log("PASS packaged browser: open, cross-origin snapshot/click/verification, PNG delivered to provider, normal exit");
} catch (error) {
  writeFileSync(join(evidence, "result.json"), JSON.stringify({ completed: false, status: "failed", steps }, null, 2));
  throw error;
} finally {
  await application?.close();
  server.closeAllConnections(); await new Promise(resolve => server.close(resolve));
  console.log(`isolated home: ${home}`);
}
