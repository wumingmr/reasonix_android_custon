import assert from "node:assert/strict";
import { test } from "node:test";
import { build } from "esbuild";
import { resolve } from "node:path";

const bundle = await build({ entryPoints: [resolve("src/main/browser/recorderRenderer.ts")], bundle: true, platform: "browser", format: "iife", target: "es2022", write: false, logLevel: "silent" });

// A static page emits only the capture's first frame and the refresh the video
// sink requests; a frame-rate-limited track can drop that refresh, and then the
// recording never receives a frame.
test("the capture track is not frame-rate limited", async () => {
  const requested: MediaStreamConstraints[] = [];
  const posted: { type: string }[] = [];
  let onMessage!: (event: unknown) => void;
  const window = { addEventListener: (_type: string, listener: (event: unknown) => void) => { onMessage = listener; } };
  const navigator = { mediaDevices: { getDisplayMedia: async (constraints: MediaStreamConstraints) => { requested.push(constraints); throw new Error("capture denied"); } } };
  new Function("window", "navigator", bundle.outputFiles[0].text)(window, navigator);
  const port = { onmessage: null as null | ((event: { data: unknown }) => Promise<void>), postMessage: (message: { type: string }) => { posted.push(message); } };
  onMessage({ source: window, data: "reasonix-recorder-port", ports: [port] });
  await port.onmessage!({ data: { type: "start", width: 1280, height: 720, cropWidth: 1, cropHeight: 1 } });
  assert.equal(requested.length, 1);
  assert.equal(requested[0].audio, false);
  assert.ok(requested[0].video === true || (typeof requested[0].video === "object" && !("frameRate" in requested[0].video)), JSON.stringify(requested[0]));
  assert.deepEqual(posted.map(message => message.type), ["ready", "error"]);
});
