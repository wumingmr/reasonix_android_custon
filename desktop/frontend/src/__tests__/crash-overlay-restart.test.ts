// Run: tsx src/__tests__/crash-overlay-restart.test.ts

import { JSDOM } from "jsdom";
import { buildCrashPayload, paintCrashOverlay } from "../lib/crash";
import { t } from "../lib/i18n";
import { installDesktopHostStub } from "./desktopHostStub";

let passed = 0;
let failed = 0;
function ok(value: boolean, label: string) {
  if (value) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed += 1;
  } else {
    process.stdout.write(`  FAIL  ${label}\n`);
    failed += 1;
  }
}

const dom = new JSDOM("<!doctype html><html><body></body></html>", { url: "http://localhost/" });
(globalThis as any).document = dom.window.document;
(globalThis as any).HTMLElement = dom.window.HTMLElement;
let reloads = 0;
// jsdom's Location.reload is unforgeable, so the overlay sees a plain window
// whose reload the test can count.
(globalThis as any).window = {
  location: { reload: () => { reloads += 1; } },
  setTimeout: (cb: () => void, ms: number) => setTimeout(cb, ms),
};

const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

function restartButton(): HTMLButtonElement | undefined {
  const overlay = document.getElementById("crash-overlay");
  return Array.from(overlay?.querySelectorAll("button") ?? []).find(
    (button) => button.textContent === t("crash.restart"),
  ) as HTMLButtonElement | undefined;
}

async function main() {
  console.log("\nCrash overlay restart");

  let restarts = 0;
  const stub = installDesktopHostStub({
    ReportCrash: async () => {},
    RestartApplication: async () => { restarts += 1; },
  });
  paintCrashOverlay(buildCrashPayload("react", new Error("render exploded")));
  const button = restartButton();
  ok(button !== undefined, "the crash overlay offers a restart action");
  button?.click();
  await flush();
  ok(restarts === 1, "restart asks the desktop host to relaunch the application");
  ok(reloads === 0, "a host relaunch that succeeds does not also reload the renderer");
  stub.uninstall();

  reloads = 0;
  const rejecting = installDesktopHostStub({
    ReportCrash: async () => {},
    RestartApplication: async () => { throw new Error("service gone"); },
  });
  paintCrashOverlay(buildCrashPayload("react", new Error("render exploded")));
  restartButton()?.click();
  await flush();
  ok(reloads === 1, "a relaunch the host refuses falls back to reloading the renderer");
  rejecting.uninstall();

  reloads = 0;
  delete (globalThis as any).window.reasonixDesktop;
  paintCrashOverlay(buildCrashPayload("react", new Error("render exploded")));
  restartButton()?.click();
  await flush();
  ok(reloads === 1, "without a desktop host the restart action reloads the renderer");

  console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total`);
  if (failed > 0) process.exit(1);
}

void main();
