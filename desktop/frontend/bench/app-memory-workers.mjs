// Measure Worker-owned listeners independently of DOM listeners. A leased
// Markdown parser may legitimately exist or be absent after navigation; its
// single worker is a bounded resource, not an accumulating DOM subscription.
export async function workerListeners(cdp) {
  const objectGroup = "app-memory-worker-evidence";
  try {
    const { result: prototype } = await cdp.send("Runtime.evaluate", { expression: "Worker.prototype", objectGroup });
    const { objects } = await cdp.send("Runtime.queryObjects", { prototypeObjectId: prototype.objectId, objectGroup });
    const { result } = await cdp.send("Runtime.getProperties", { objectId: objects.objectId, ownProperties: true });
    const workers = result.filter(property => /^\d+$/.test(property.name));
    return await Promise.all(workers.map(async ({ value }) => {
      const { listeners } = await cdp.send("DOMDebugger.getEventListeners", { objectId: value.objectId });
      return listeners.map(listener => listener.type).sort();
    }));
  } finally {
    await cdp.send("Runtime.releaseObjectGroup", { objectGroup });
  }
}

export function boundedParserWorker(sample) {
  // Older evidence did not attribute workers: leave its raw counters intact.
  if (sample.workers === undefined) return true;
  return Array.isArray(sample.workers) && sample.workers.length <= 1 && sample.workers.every(events =>
    Array.isArray(events) && events.filter(type => type === "message").length <= 1
    && events.filter(type => type === "error").length <= 2
    && events.every(type => type === "message" || type === "error"));
}

export function domListenerCount(sample) {
  const owned = boundedParserWorker(sample) ? (sample.workers ?? []).flat().length : 0;
  return sample.dom?.jsEventListeners - owned;
}
