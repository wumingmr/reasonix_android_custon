import assert from "node:assert/strict";

export const TRANSCRIPT_PHASES = ["initialLoad", "historyPaging", "streaming", "input"];
export const LONG_TASK_LIMIT_MS = 500;
export const INPUT_P95_LIMIT_MS = 200;

export function percentile(values, ratio = 0.95) {
  return [...values].sort((a, b) => a - b)[Math.max(0, Math.ceil(values.length * ratio) - 1)] ?? 0;
}

function validateAttempt(attempt) {
  assert.ok(attempt && Number.isInteger(attempt.attempt), "performance attempt is missing its ordinal");
  assert.deepEqual(attempt.errors, [], `performance attempt ${attempt.attempt} reported browser errors`);
  assert.ok(attempt.inputCount >= 30, `performance attempt ${attempt.attempt} has incomplete input samples`);
  assert.ok(Number.isFinite(attempt.inputP95), `performance attempt ${attempt.attempt} is missing input P95`);
  for (const phase of TRANSCRIPT_PHASES) {
    const sample = attempt.phases?.[phase];
    assert.ok(sample && Number.isFinite(sample.elapsedMs), `performance attempt ${attempt.attempt} is missing ${phase}`);
    assert.ok(Array.isArray(sample.longTasks), `performance attempt ${attempt.attempt} is missing ${phase} long tasks`);
    assert.ok(Number.isFinite(sample.longTaskMax), `performance attempt ${attempt.attempt} is missing ${phase} maximum`);
  }
}

export function needsBoundedRetry(attempt) {
  validateAttempt(attempt);
  return attempt.inputP95 > INPUT_P95_LIMIT_MS
    || (attempt.longTaskSupported
      && TRANSCRIPT_PHASES.some(phase => attempt.phases[phase].longTaskMax > LONG_TASK_LIMIT_MS));
}

export function decideTranscriptPerformance(attempts) {
  assert.ok(attempts.length === 1 || attempts.length === 3, "performance decision requires one or three attempts");
  attempts.forEach(validateAttempt);
  const firstExceeded = needsBoundedRetry(attempts[0]);
  if (!firstExceeded) {
    assert.equal(attempts.length, 1, "passing first attempt must not be retried");
    return {
      status: "passed-first-attempt",
      passed: true,
      medians: phaseMedians(attempts),
      inputP95Median: inputP95Median(attempts),
    };
  }
  assert.equal(attempts.length, 3, "an over-limit first attempt requires exactly two retries");
  const medians = phaseMedians(attempts);
  const inputMedian = inputP95Median(attempts);
  const passed = inputMedian <= INPUT_P95_LIMIT_MS
    && TRANSCRIPT_PHASES.every(phase => medians[phase] <= LONG_TASK_LIMIT_MS);
  return {
    status: passed ? "passed-after-bounded-retry" : "failed-sustained-regression",
    passed,
    medians,
    inputP95Median: inputMedian,
  };
}

export async function collectTranscriptPerformance(sampleAttempt) {
  const first = await sampleAttempt(1);
  validateAttempt(first);
  const attempts = [first];
  if (needsBoundedRetry(attempts[0])) {
    const second = await sampleAttempt(2);
    validateAttempt(second);
    attempts.push(second);
    const third = await sampleAttempt(3);
    validateAttempt(third);
    attempts.push(third);
  }
  return { attempts, decision: decideTranscriptPerformance(attempts) };
}

function phaseMedians(attempts) {
  return Object.fromEntries(TRANSCRIPT_PHASES.map(phase => [phase,
    percentile(attempts.map(attempt => attempt.phases[phase].longTaskMax), 0.5)]));
}

function inputP95Median(attempts) {
  return percentile(attempts.map(attempt => attempt.inputP95), 0.5);
}

export function formatPerformanceSummary(browser, turns, decision, attempts) {
  const label = decision.status === "passed-first-attempt" ? "passed on the first attempt"
    : decision.status === "passed-after-bounded-retry" ? "passed after bounded retry"
      : "failed with a sustained regression";
  const medians = `${TRANSCRIPT_PHASES.map(phase => `${phase}=${decision.medians[phase]}ms`).join(", ")}, inputP95=${decision.inputP95Median}ms`;
  const samples = attempts.map(attempt => `#${attempt.attempt} [${TRANSCRIPT_PHASES
    .map(phase => `${phase}=${attempt.phases[phase].longTaskMax}ms`).join(", ")}, inputP95=${attempt.inputP95}ms]`).join("; ");
  return `- ${browser}, ${turns} turns: **${label}**; medians: ${medians}; samples: ${samples}`;
}

export async function installTranscriptPerformanceObserver(page) {
  await page.addInitScript(() => {
    window.chatMetrics = { inputs: [], tasks: [], longTaskSupported: PerformanceObserver.supportedEntryTypes.includes("longtask") };
    if (window.chatMetrics.longTaskSupported) {
      new PerformanceObserver(list => window.chatMetrics.tasks.push(...list.getEntries().map(entry => ({
        startTime: entry.startTime,
        duration: entry.duration,
      })))).observe({ type: "longtask" });
    }
    document.addEventListener("keydown", event => {
      if (!event.target.matches("textarea.composer__input")) return;
      // The first animation frame is the browser's next paint opportunity for
      // this input. A second frame measures an unrelated scheduling interval
      // and made the strict input gate depend on runner descheduling.
      const start = event.timeStamp;
      requestAnimationFrame(() => window.chatMetrics.inputs.push(performance.now() - start));
    }, true);
  });
}

async function measurePhase(page, operation) {
  const start = await page.evaluate(() => performance.now());
  const detail = await operation();
  const end = await page.evaluate(() => performance.now());
  await page.waitForTimeout(0);
  const tasks = await page.evaluate(({ start, end }) => window.chatMetrics.tasks
    .filter(task => task.startTime >= start && task.startTime < end), { start, end });
  return {
    elapsedMs: end - start,
    longTasks: tasks,
    longTaskMax: Math.max(0, ...tasks.map(task => task.duration)),
    ...detail,
  };
}

export async function measureTranscriptPerformance({ page, turns, attempt, frame, errors }) {
  const errorStart = errors.length;
  await page.evaluate(() => { window.chatMetrics.inputs = []; window.chatMetrics.tasks = []; });
  const phases = {};
  phases.initialLoad = await measurePhase(page, async () => {
    await page.evaluate(count => window.chatFixture.reset(count), turns);
    await page.locator(`[data-chat-anchor-key="u${Math.max(0, turns - 60)}"][data-chat-kind="user"]`).waitFor();
    await frame(page);
    return {};
  });
  phases.historyPaging = await measurePhase(page, async () => {
    const pages = [];
    for (let loaded = 60; loaded < turns; loaded += 60) {
      const pageStart = Date.now();
      await page.evaluate(() => window.chatFixture.older());
      const nextLoaded = Math.min(turns, loaded + 60);
      await page.locator(`[data-chat-anchor-key="u${turns - nextLoaded}"][data-chat-kind="user"]`).waitFor();
      await frame(page);
      pages.push(Date.now() - pageStart);
    }
    assert.equal(await page.locator(".transcript__window-item").count(), 0);
    assert.equal(await page.locator('[data-chat-kind="user"]').count(), turns, `${turns} turns fully mounted`);
    return { pages };
  });
  phases.streaming = await measurePhase(page, async () => {
    for (let index = 0; index < 30; index++) await page.evaluate(value => window.chatFixture.tick(value), index);
    await frame(page);
    return {};
  });
  const input = page.locator("textarea.composer__input:not(.composer__input--measure)");
  phases.input = await measurePhase(page, async () => {
    await input.fill("");
    await page.evaluate(() => { window.chatMetrics.inputs = []; window.chatFixture.tick(0); });
    for (let index = 0; index < 30; index++) {
      await page.evaluate(value => window.chatFixture.tick(value), index);
      await input.press("a");
    }
    await frame(page);
    assert.equal(await input.inputValue(), "a".repeat(30));
    return {};
  });
  const metrics = await page.evaluate(() => window.chatMetrics);
  const attemptErrors = errors.slice(errorStart);
  const result = {
    attempt,
    turns,
    phases,
    inputs: metrics.inputs,
    inputCount: metrics.inputs.length,
    inputP95: percentile(metrics.inputs),
    longTaskSupported: metrics.longTaskSupported,
    errors: attemptErrors,
    dom: await page.locator("*").count(),
  };
  return result;
}
