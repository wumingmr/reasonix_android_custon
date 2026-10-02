/// <reference lib="dom" />
// This file is compiled independently, without Node integration or page code.
window.addEventListener("message", event => {
  if (event.source !== window || event.data !== "reasonix-recorder-port" || !event.ports[0]) return;
  const port = event.ports[0];
  let recorder: MediaRecorder | undefined;
  let stream: MediaStream | undefined;
  let output: MediaStream | undefined;
  let paintTimer: ReturnType<typeof setInterval> | undefined;
  let bytes = 0;
  let chunks: Blob[] = [];
  let started = 0;
  let announced = false;
  let cancelled = false;
  let writes = Promise.resolve();
  const cleanup = () => { clearInterval(paintTimer); output?.getTracks().forEach(track => track.stop()); stream?.getTracks().forEach(track => track.stop()); stream = undefined; };
  const fail = (error: unknown) => { cancelled = true; cleanup(); port.postMessage({ type: "error", message: String(error).slice(0, 300) }); };
  port.onmessage = async ({ data }) => {
    if (data.type === "cancel") { cancelled = true; if (recorder?.state === "recording") recorder.stop(); cleanup(); return; }
    if (data.type === "stop") { if (recorder?.state === "recording") recorder.stop(); return; }
    if (data.type !== "start" || recorder) return;
    try {
      // No frameRate: a static page sends only its first frame and the refresh a
      // new sink asks for, and a capped track drops that refresh when it trails
      // the first by less than a frame period. The paint interval sets output rate.
      stream = await navigator.mediaDevices.getDisplayMedia({ video: true, audio: false });
      if (cancelled) { cleanup(); return; }
      const input = document.createElement("video"); input.muted = true; input.srcObject = stream;
      await input.play();
      const canvas = document.createElement("canvas"); canvas.width = data.width; canvas.height = data.height;
      const context = canvas.getContext("2d");
      if (!context || !canvas.width || !canvas.height) throw new Error("invalid recording viewport");
      const paint = () => {
        if (!input.videoWidth || !input.videoHeight) return;
        context.drawImage(input, 0, 0, input.videoWidth * data.cropWidth, input.videoHeight * data.cropHeight, 0, 0, canvas.width, canvas.height);
        (output?.getVideoTracks()[0] as CanvasCaptureMediaStreamTrack | undefined)?.requestFrame();
      };
      output = canvas.captureStream(0);
      const mimeType = ["video/webm;codecs=vp8", "video/webm"].find(type => MediaRecorder.isTypeSupported(type));
      if (!mimeType) throw new Error("WebM recording is unavailable");
      recorder = new MediaRecorder(output, { mimeType, videoBitsPerSecond: 3_000_000 });
      recorder.ondataavailable = event => {
        if (cancelled || !event.data.size) return;
        bytes += event.data.size;
        if (bytes > 64 * 1024 * 1024) { fail("recording exceeded 64 MiB"); recorder?.stop(); return; }
        chunks.push(event.data);
        if (!announced) { announced = true; port.postMessage({ type: "started", elapsedMs: performance.now() - started }); }
        // Electron MessagePortMain can drop transferred ArrayBuffers. Structured
        // clone preserves chunk ordering across the DOM-to-main boundary.
        writes = writes.then(async () => { const chunk = await event.data.arrayBuffer(); port.postMessage({ type: "chunk", data: chunk }); });
      };
      recorder.onerror = event => fail(event.error);
      recorder.onstop = async () => {
        cleanup();
        if (cancelled) return;
        try {
          await writes;
          const blob = new Blob(chunks, { type: mimeType }); chunks = [];
          const url = URL.createObjectURL(blob);
          const video = document.createElement("video"); video.muted = true; video.src = url;
          try {
            await new Promise<void>((resolve, reject) => { video.onloadeddata = () => resolve(); video.onerror = () => reject(new Error("recorded WebM cannot be decoded")); });
            if (!video.videoWidth || !video.videoHeight) throw new Error("recording has no video dimensions");
            port.postMessage({ type: "stopped", width: video.videoWidth, height: video.videoHeight, durationMs: performance.now() - started, bytes });
          } finally { video.removeAttribute("src"); video.load(); URL.revokeObjectURL(url); }
        } catch (error) { fail(error); }
      };
      stream.getVideoTracks()[0]?.addEventListener("ended", () => { if (recorder?.state === "recording") fail("target page capture ended"); });
      started = performance.now(); recorder.start(250);
      paint(); paintTimer = setInterval(paint, 40);
    } catch (error) { fail(error); }
  };
  port.postMessage({ type: "ready" });
}, { once: true });
