import { app } from "./bridge";
import { asArray } from "./array";
import { entriesFor } from "./canonicalTranscriptBackend";
import { readBoundHistoryWindow } from "./historyReadBinding";
import type { HistoryWindowPage, PersistentMessage } from "../generated/desktopContract.generated";
import type { HistoryWindowPageView, HistoryWindowRequestView } from "./types";

function unsupportedWindow(): HistoryWindowPageView {
  return {
    entries: [], status: "unsupported", olderCursor: "", newerCursor: "",
    hasOlder: false, hasNewer: false, totalTurns: 0, startTurn: 0, endTurn: 0,
    revision: 0, revisionKnown: false, digest: "",
  };
}

export async function readCanonicalHistoryWindow(tabId: string, req: HistoryWindowRequestView, remote: boolean): Promise<HistoryWindowPageView> {
  let page: HistoryWindowPage;
  if (remote) {
    if (typeof app.RemoteSessionHistoryWindowForTab !== "function") {
      return unsupportedWindow();
    }
    page = await app.RemoteSessionHistoryWindowForTab(tabId, req);
  } else {
	const bound = await readBoundHistoryWindow(tabId, req);
	if (bound && "entries" in bound) return bound;
	if (bound) page = bound;
	else {
    if (typeof app.SessionHistoryWindowForTab !== "function") {
      return unsupportedWindow();
    }
    page = await app.SessionHistoryWindowForTab(tabId, req);
	}
  }
  const status = (page.status || "ready") as HistoryWindowPageView["status"];
  if (status === "unsupported") {
    return unsupportedWindow();
  }
  const entries = entriesFor(asArray<PersistentMessage>(page.messages), page.snapshotSequence);
  const turns = entries.map(entry => entry.turn).filter(turn => turn > 0);
  return {
    entries,
    status,
    olderCursor: page.olderCursor ?? "",
    newerCursor: page.newerCursor ?? "",
    hasOlder: Boolean(page.hasOlder),
    hasNewer: Boolean(page.hasNewer),
    totalTurns: page.totalTurns ?? (turns.length > 0 ? Math.max(...turns) : 0),
    startTurn: turns.length > 0 ? Math.min(...turns) : 0,
    endTurn: turns.length > 0 ? Math.max(...turns) : 0,
    revision: page.snapshotSequence ?? 0,
    revisionKnown: (page.snapshotSequence ?? 0) > 0,
    digest: page.generation ?? "",
  };
}
