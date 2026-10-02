// Run: tsx src/__tests__/goal-lifecycle-actions.test.tsx

import { JSDOM } from "jsdom";

const dom = new JSDOM("<!doctype html><html><body><div id=\"root\"></div></body></html>", { url: "http://localhost/" });
(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.window = dom.window as unknown as Window & typeof globalThis;
globalThis.document = dom.window.document;
globalThis.HTMLElement = dom.window.HTMLElement;
globalThis.Event = dom.window.Event;

const React = await import("react");
const { act } = React;
const { createRoot } = await import("react-dom/client");
const { GoalLifecycleActions } = await import("../components/GoalLifecycleActions");
const { LocaleProvider } = await import("../lib/i18n");

let edit: { objective: string; maxGoalRounds: number | null } | undefined;
let paused = 0;
window.prompt = () => { throw new Error("prompt() is not supported."); };
window.alert = () => { throw new Error("alert() is not supported."); };

await act(async () => {
  createRoot(document.getElementById("root")!).render(
    <LocaleProvider>
      <GoalLifecycleActions
        goalView={{
          id: "goal-1", revision: 3, objective: "finish the migration", phase: "active",
          maxGoalRounds: null, roundsStarted: 2, createdAt: "2026-01-01T00:00:00Z",
          updatedAt: "2026-01-01T00:00:00Z", activation: "armed",
        }}
        goalStatus="running"
        running
        onEditGoal={(objective, maxGoalRounds) => { edit = { objective, maxGoalRounds }; }}
        onPauseGoal={() => { paused += 1; }}
        onResumeGoal={() => {}}
        onStopGoal={() => {}}
      />
    </LocaleProvider>,
  );
});

const buttons = Array.from(document.querySelectorAll<HTMLButtonElement>("button"));
const editButton = buttons.find((button) => button.textContent === "Edit goal");
const pauseButton = buttons.find((button) => button.textContent === "Pause goal");
if (!editButton || !pauseButton) throw new Error("active goal lifecycle actions did not render");
if (pauseButton.disabled) throw new Error("running automatic Goal round is not pausable");

const setValue = async (el: HTMLInputElement | HTMLTextAreaElement, value: string) => {
  const proto = el instanceof dom.window.HTMLTextAreaElement ? dom.window.HTMLTextAreaElement.prototype : dom.window.HTMLInputElement.prototype;
  await act(async () => {
    Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, value);
    el.dispatchEvent(new dom.window.Event("input", { bubbles: true }));
  });
};

await act(async () => { editButton.click(); });
const objectiveInput = document.querySelector<HTMLTextAreaElement>("[data-goal-edit-objective]");
const roundsInput = document.querySelector<HTMLInputElement>("[data-goal-edit-rounds]");
if (!objectiveInput || !roundsInput) throw new Error("edit goal did not open an in-app form");
if (objectiveInput.value !== "finish the migration") throw new Error("objective not prefilled");
await setValue(roundsInput, "0");
const form = document.querySelector<HTMLFormElement>("form[data-goal-edit-form]")!;
await act(async () => { form.dispatchEvent(new dom.window.Event("submit", { bubbles: true, cancelable: true })); });
if (edit) throw new Error("invalid rounds must not submit");
if (!document.querySelector("[data-goal-edit-error]")) throw new Error("invalid rounds error not shown");
await setValue(objectiveInput, "finish the migration safely");
await setValue(roundsInput, "12");
await act(async () => { form.dispatchEvent(new dom.window.Event("submit", { bubbles: true, cancelable: true })); });
if (document.querySelector("form[data-goal-edit-form]")) throw new Error("form should close after save");
if (edit?.objective !== "finish the migration safely" || edit.maxGoalRounds !== 12) {
  throw new Error(`edit action returned ${JSON.stringify(edit)}`);
}
await act(async () => { pauseButton.click(); });
if (paused !== 1) throw new Error("pause action was not delivered");
console.log("goal lifecycle actions: PASS");
