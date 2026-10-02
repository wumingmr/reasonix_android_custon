export async function launchTranscriptRetryHost(engine, { headless, viewport }) {
  const browser = await engine.launch({ headless });
  let closed = false;
  return {
    async run(sample) {
      if (closed) throw new Error("transcript retry host is closed");
      const context = await browser.newContext({ viewport });
      try {
        const page = await context.newPage();
        return await sample(page, context);
      } finally {
        await context.close();
      }
    },
    async close() {
      if (closed) return;
      closed = true;
      await browser.close();
    },
  };
}
