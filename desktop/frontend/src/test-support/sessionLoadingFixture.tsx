import { ChatPaneRegion } from "../app-shell/ChatPaneRegion";
import { RemoteSessionSurface } from "../components/RemoteSessionSurface";
import { Transcript } from "../components/Transcript";
import { initialState, type Item } from "../lib/useController";
import type { RemoteSessionApi } from "../lib/useRemoteSession";
import type { SessionAvailability } from "../lib/sessionAvailability";
import { useT } from "../lib/i18n";

export type LoadingFixtureInput = {
  surface: "local" | "remote" | "transcript";
  identity: string;
  generation?: number;
  phase: "loading" | "ready" | "error";
  cached?: boolean;
  source?: SessionAvailability["source"];
  onRetry?: () => Promise<void>;
};

/** Real production surfaces with controlled data completion, shared by DOM/browser checks. */
export function SessionLoadingFixture({ surface, identity, generation = 1, phase, cached = false, source = "history", onRetry = async () => {} }: LoadingFixtureInput) {
  const t = useT();
  const items: Item[] = cached || phase === "ready" ? [{ kind: "user", id: `${identity}-user`, text: `Content ${identity}` }] : [];
  const state = { ...initialState, items, hydrating: phase === "loading" && source === "history" };
  const availability: SessionAvailability = { kind: phase, source, detail: phase === "error" ? "Fixture read failed" : undefined };
  if (surface === "transcript") return <Transcript items={items} tabId={identity} geometrySessionKey={`${identity}:${generation}`}
    hydrating={phase === "loading"} onPrompt={() => {}} />;
  if (surface === "remote") {
    const session = {
      state: phase === "loading" && source === "connection" ? "connecting" : "ready",
      hydrated: phase === "ready", error: phase === "error" ? "Fixture read failed" : "",
      transcript: state, surfaceGeneration: generation, retryHydration: onRetry,
    } as RemoteSessionApi;
    return <RemoteSessionSurface tab={{ id: identity, scope: "project", workspaceRoot: "/fixture", workspaceName: "Fixture",
      topicId: identity, topicTitle: identity, label: "Fixture", ready: true, running: false, mode: "normal", active: true, cwd: "/fixture",
      remote: { hostId: "fixture", workspace: "/fixture" } }} session={session} />;
  }
  return <ChatPaneRegion transitioning={false} imDetail={null} remote={undefined} t={t}
    transcript={{ state, items, tabId: identity, geometrySessionKey: `${identity}:${generation}`,
      footerHeight: 100, transcriptHydrating: state.hydrating, navigationDataReady: phase === "ready",
      readOnly: false, controllerReady: phase === "ready", hydratePlaceholderActive: cached && phase === "loading",
      clearContextPending: false, availability, rewind: { stateActive: false, committing: false },
      invocationMetadata: undefined, surfaceCommitToken: undefined, liveStore: undefined }}
    onRetryHistory={onRetry} commands={{ onPrompt: () => {}, onFork: () => {},
      onLoadOlderHistory: undefined, onLoadNewerHistory: undefined, onSurfacePaintReady: undefined }} />;
}
