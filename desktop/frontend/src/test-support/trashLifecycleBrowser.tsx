import { createRoot } from "react-dom/client";
import { TrashPage } from "../components/TrashPage";
import { LocaleProvider } from "../lib/i18n";
import type { SessionLifecycleRequest } from "../generated/desktopContract.generated";
import { installDesktopHostStub } from "../__tests__/desktopHostStub";
import "../styles.css";

let generation = 7, targetGeneration = 7;
let unknown = false, preview = true;
let finishPreview: (() => void) | undefined;
const requests: SessionLifecycleRequest[] = [];
const ref = { hostId: "local", sessionId: "fixture-session" };
const host = installDesktopHostStub({
  ListTrashEntries: async () => ({ generation, items: [{ id: ref.sessionId, ref, title: "Fixture conversation", workspaceTitle: "Global", archivedAt: 0, canPurge: true, canRestore: preview, canPreview: preview, health: "ready" }] }),
  ReadSessionHistory: async () => new Promise(resolve => { finishPreview = () => resolve({ messages: [{ role: "user", content: "late fixture history" }] }); }),
  ApplySessionLifecycle: async (request: SessionLifecycleRequest) => {
    requests.push(structuredClone(request));
    if (unknown) { unknown = false; throw new Error("network outcome unknown"); }
    const committed = request.expectedGeneration >= targetGeneration;
    return { operationId: request.operationId, generation, committed, items: request.targets.map(target => ({ target, committed, retryable: false, errorCode: committed ? "" : "state_conflict" })) };
  },
});
Object.assign(window, { trashFixture: {
  requests,
  changeTarget() { targetGeneration = ++generation; host.emit("project-tree:changed"); },
  changeOther() { generation++; host.emit("project-tree:changed"); },
  loseResult() { unknown = true; },
  invalidatePreview() { preview = false; host.emit("project-tree:changed"); },
  finishPreview() { finishPreview?.(); },
} });
createRoot(document.getElementById("root")!).render(<LocaleProvider><TrashPage active onBack={() => {}}
  onOpenSession={async () => {}} list={async () => []} restore={async () => {}} purge={async () => {}} /></LocaleProvider>);
