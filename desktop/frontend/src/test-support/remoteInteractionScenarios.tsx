import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { TabMeta } from "../lib/types";
import type { AppBindings } from "../lib/bridge";

type Assert = (value: boolean, label: string) => void;
type Emit = (tabId: string, channel: "event" | "state", payload: unknown) => void;

export function makeExactRemoteInteractionBindings(options: {
  tape: string[];
  failApproval: () => boolean;
  failAnswer: () => boolean;
  waitApproval: () => Promise<void>;
  waitAnswer: () => Promise<void>;
  waitForm: () => Promise<void>;
  resolved: (tabId: string, promptId: string, turnId?: string) => void;
}): Pick<AppBindings, "ResolveRemoteTabPromptExact" | "SubmitRemoteTabExtensionFormExact"> {
  return {
    async ResolveRemoteTabPromptExact(target, answer) {
      if (target.kind === "plan") options.tape.push(`plan-decision:${target.tabId}:${target.promptId}:${answer.action ?? ""}:${answer.feedback ?? ""}`);
      else if (target.kind === "ask") {
        options.tape.push(`answer:${target.tabId}:${target.promptId}:${JSON.stringify(answer.questions ?? [])}`);
        if (options.failAnswer()) throw new Error("remote answer failed");
        await options.waitAnswer();
      } else if (target.kind === "approval" || target.kind === "recovery") {
        const decision = answer.allow ? answer.persist ? "persist" : answer.session ? "session" : "allow" : "deny";
        options.tape.push(`approve:${target.tabId}:${target.promptId}:${decision}`);
        if (options.failApproval()) throw new Error("tunnel write failed");
        await options.waitApproval();
      } else options.tape.push(`mcp-answer:${target.tabId}:${target.promptId}:${answer.action ?? ""}`);
      options.resolved(target.tabId, target.promptId, target.turnId);
    },
    async SubmitRemoteTabExtensionFormExact(target, values) {
      options.tape.push(`extension-form:${target.tabId}:${target.pluginId}:${target.surfaceId}:${JSON.stringify(values)}`);
      await options.waitForm();
    },
  };
}

export async function runRemoteExtensionFormScenarios(options: {
  emit: Emit;
  flush: () => Promise<void>;
  ok: Assert;
  tape: string[];
  blockForm: (blocked: boolean) => void;
  releaseForm: () => void;
}): Promise<void> {
  const { emit, flush, ok, tape } = options;
  await act(async () => {
    emit("tab-remote-1", "event", { kind: "extension_surface", extension: {
      pluginId: "remote-plugin", surfaceId: "setup", generation: 1, formInstanceId: "setup-instance", kind: "form",
      form: { title: "Remote setup", fields: [{ key: "region", label: "Region", kind: "input", required: true, default: "us-west" }] },
    } });
    await flush();
  });
  const form = document.querySelector(".extension-form");
  ok(Boolean(form) && document.body.textContent?.includes("Remote setup") === true, "remote extension form renders on the shared surface");
  await act(async () => { form?.closest(".prompt-shelf")?.querySelector<HTMLButtonElement>(".decision-confirm-bar__confirm")?.click(); await flush(); });
  ok(tape.includes('extension-form:tab-remote-1:remote-plugin:setup:{"region":"us-west"}'), "remote extension form submits through the Serve proxy");
  ok(!document.querySelector(".extension-form"), "remote extension form clears after an accepted submission");

  options.blockForm(true);
  await act(async () => {
    emit("tab-remote-1", "event", { kind: "extension_surface", extension: {
      pluginId: "remote-plugin", surfaceId: "reused", generation: 7, formInstanceId: "old-instance", kind: "form",
      form: { title: "Old publication", fields: [{ key: "value", label: "Value", kind: "input", default: "old" }] },
    } });
    await flush();
  });
  await act(async () => { document.querySelector(".extension-form")?.closest(".prompt-shelf")?.querySelector<HTMLButtonElement>(".decision-confirm-bar__confirm")?.click(); await Promise.resolve(); });
  ok(document.querySelector(".extension-form")?.closest(".prompt-shelf")?.querySelector<HTMLButtonElement>(".decision-confirm-bar__confirm")?.disabled === true, "remote form busy state belongs to the submitting publication");
  await act(async () => {
    emit("tab-remote-1", "event", { kind: "extension_surface", extension: {
      pluginId: "remote-plugin", surfaceId: "reused", generation: 7, formInstanceId: "replacement-instance", kind: "form",
      form: { title: "Replacement publication", fields: [{ key: "value", label: "Value", kind: "input", default: "new" }] },
    } });
    await flush();
  });
  ok(document.body.textContent?.includes("Replacement publication") === true && document.querySelector(".extension-form")?.closest(".prompt-shelf")?.querySelector<HTMLButtonElement>(".decision-confirm-bar__confirm")?.disabled === false, "a replacement remote form does not inherit the old instance busy state");
  options.blockForm(false);
  await act(async () => { options.releaseForm(); await flush(); });
  ok(document.body.textContent?.includes("Replacement publication") === true, "late completion from the old form instance cannot clear its replacement");
  await act(async () => { document.querySelector(".extension-form")?.closest(".prompt-shelf")?.querySelector<HTMLButtonElement>(".decision-confirm-bar__confirm")?.click(); await flush(); });
  ok(!document.querySelector(".extension-form"), "replacement remote form clears only after its own submission");
}

export async function runLegacyRemoteCapabilityScenario(options: {
  baseTab: TabMeta;
  render: (root: Root, tab: TabMeta) => void;
  emit: Emit;
  flush: () => Promise<void>;
  ok: Assert;
  tape: string[];
}): Promise<void> {
  const container = document.createElement("div");
  document.body.appendChild(container);
  const root = createRoot(container);
  const tab: TabMeta = { ...options.baseTab, id: "tab-legacy-capabilities", sessionId: "legacy-session", sessionGeneration: 1,
    interactionTargetSupported: false, extensionFormInstanceSupported: false };
  await act(async () => { options.render(root, tab); await options.flush(); });
  await act(async () => {
    options.emit(tab.id, "event", { kind: "approval_request", approval: { id: "legacy-approval", tool: "bash", subject: "must remain fenced" } });
    options.emit(tab.id, "event", { kind: "extension_surface", extension: {
      pluginId: "legacy-plugin", surfaceId: "legacy-form", generation: 1, formInstanceId: "legacy-instance", kind: "form",
      form: { title: "Legacy form", fields: [{ key: "value", label: "Value", kind: "input", default: "blocked" }] },
    } });
    await options.flush();
  });
  const approval = container.querySelector<HTMLButtonElement>(".remote-surface__approval .prompt-action");
  const form = container.querySelector(".extension-form")?.closest(".prompt-shelf")?.querySelector<HTMLButtonElement>(".decision-confirm-bar__confirm");
  options.ok(container.textContent?.includes("must be upgraded") === true, "old Serve shows an upgrade notice only for unsafe interactive cards");
  options.ok(approval?.closest("fieldset")?.disabled === true && form?.disabled === true, "old Serve keeps exact prompt and form actions disabled");
  const before = options.tape.filter((entry) => entry.includes(tab.id) && (entry.startsWith("approve:") || entry.startsWith("extension-form:"))).length;
  await act(async () => { approval?.click(); form?.click(); await options.flush(); });
  options.ok(options.tape.filter((entry) => entry.includes(tab.id) && (entry.startsWith("approve:") || entry.startsWith("extension-form:"))).length === before, "old Serve card clicks never fall back to legacy mutation endpoints");
  await act(async () => root.unmount());
  container.remove();
}
