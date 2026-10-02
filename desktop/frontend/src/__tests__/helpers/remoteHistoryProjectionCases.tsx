import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { LocaleProvider } from "../../lib/i18n";
import { useRemoteSession, type RemoteSessionApi } from "../../lib/useRemoteSession";

/** Cases that assert what a remote snapshot projects into the transcript. */
interface RemoteProjectionHarness {
  ok: (value: boolean, label: string) => void;
  flush: () => Promise<void>;
}

/** History from a serve keeps the tool args and output its transcript shows. */
export async function runRemoteToolHistoryCase({ ok, flush }: RemoteProjectionHarness) {
  let toolProbe: RemoteSessionApi | undefined;
  function ToolProbe() {
    toolProbe = useRemoteSession("tab-tool-history");
    return null;
  }
  const toolRoot = createRoot(document.createElement("div"));
  await act(async () => {
    toolRoot.render(<LocaleProvider><ToolProbe /></LocaleProvider>);
    await flush();
  });
  const remoteTool = toolProbe?.transcript.items.find((item) => item.kind === "tool" && item.id === "remote-tool");
  ok(remoteTool?.kind === "tool" && remoteTool.args.includes("go test ./...")
    && remoteTool.output === "remote tool output" && remoteTool.dataArchived !== true,
    "remote history retains expandable tool args and output without a local rehydrate endpoint");
  await act(async () => toolRoot.unmount());
}

// Pending prompt frames retained by Desktop are replayed by the next snapshot,
// which restores decisions missed while the tab had no frontend listener.
export async function runRemotePendingPromptReplayCase({ ok, flush }: RemoteProjectionHarness) {
  let replayProbe: RemoteSessionApi | undefined;
  function ReplayProbe() { replayProbe = useRemoteSession("tab-replay"); return null; }
  const replayRoot = createRoot(document.createElement("div"));
  await act(async () => {
    replayRoot.render(<LocaleProvider><ReplayProbe /></LocaleProvider>);
    await flush();
  });
  ok(replayProbe?.transcript.approval?.id === "replayed-approval", "snapshot replays a prompt emitted while the remote tab was inactive");
  ok(replayProbe?.transcript.extensionForm?.pluginId === "replayed-plugin" && replayProbe.transcript.extensionForm.surfaceId === "replayed-form", "snapshot replays an extension form emitted while the remote tab was inactive");
  await act(async () => { replayProbe?.drainApprovals(["different-approval"]); await flush(); });
  ok(replayProbe?.transcript.approval?.id === "replayed-approval", "a remote mode transaction preserves approvals it did not drain");
  await act(async () => { replayProbe?.drainApprovals(["replayed-approval"]); await flush(); });
  ok(replayProbe?.transcript.approval === undefined, "a remote mode transaction clears the exact approval it auto-allowed");
  await act(async () => replayRoot.unmount());
}
