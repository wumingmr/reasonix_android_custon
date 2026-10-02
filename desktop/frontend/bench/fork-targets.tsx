// Browser fixture for the transcript's turn-fork entry. It renders the real
// Transcript with the props the app shell passes and drives the real
// create-only binding, so a browser run shows exactly the states a user sees.
import { useCallback, useLayoutEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import { Transcript } from "../src/components/Transcript";
import { LocaleProvider, useI18n } from "../src/lib/i18n";
import { app } from "../src/lib/bridge";
import { forkCreateFailureText, type ForkTargetSetView, type ForkTargetView } from "../src/lib/forkTargets";
import type { Item } from "../src/lib/useController";
import "../src/styles.css";

// One persisted turn record: the boundary a fork may cut at, addressed by the
// message identity of the turn's final answer.
type TurnRecord = { turnId: string; user: string; answer: string; messageId?: string; open?: boolean };
type SourceName = "completed" | "open" | "recordless";

const SOURCES: Record<SourceName, { records: TurnRecord[]; verifiable: boolean; running: boolean }> = {
  completed: {
    records: [
      { turnId: "turn-1", user: "Explain the retry policy", answer: "The retry policy backs off exponentially.", messageId: "a1" },
      { turnId: "turn-2", user: "Now summarize it", answer: "It retries with exponential backoff and a cap.", messageId: "a2" },
    ],
    verifiable: true,
    running: false,
  },
  open: {
    records: [
      { turnId: "turn-1", user: "Explain the retry policy", answer: "The retry policy backs off exponentially.", messageId: "a1" },
      { turnId: "turn-2", user: "Now summarize it", answer: "Summarizing the policy while the turn runs…", open: true },
    ],
    verifiable: true,
    running: true,
  },
  recordless: {
    records: [{ turnId: "turn-1", user: "Legacy question", answer: "A legacy answer with no turn record." }],
    verifiable: false,
    running: false,
  },
};

// The host derives one target per persisted turn; a turn that has not closed has
// no boundary, and a recordless source proves none at all.
function targetsOf(source: SourceName, records: TurnRecord[]): ForkTargetSetView {
  return {
    sourceSessionId: `bench-${source}`, sessionGeneration: 1,
    targets: records.map((record, index) => record.open || !record.messageId
      ? { sourceSessionId: `bench-${source}`, sessionGeneration: 1, turnId: record.turnId, boundarySequence: 0, turnNumber: index + 1, status: record.open ? "in_progress" : "committed", available: false, reason: "turn_open" }
      : { sourceSessionId: `bench-${source}`, sessionGeneration: 1, turnId: record.turnId, boundarySequence: (index + 1) * 3, turnNumber: index + 1, status: "committed", messageId: record.messageId, available: true }),
    verifiable: SOURCES[source].verifiable,
  };
}

// Items carry the identity the transcript keys on: `m:<messageId>` when the
// record keeps one, the positional history key when it does not.
function itemsOf(records: TurnRecord[]): Item[] {
  return records.flatMap((record): Item[] => [
    { kind: "user", id: `u:${record.turnId}`, text: record.user, checkpointTurn: Number(record.turnId.split("-")[1]) },
    { kind: "assistant", id: record.messageId ? `m:${record.messageId}` : `h:${record.turnId}`, text: record.answer, reasoning: "", streaming: Boolean(record.open) },
  ]);
}

declare global {
  interface Window {
    forkFixture: {
      source(name: SourceName): void;
      locale(locale: "en" | "zh"): void;
      calls(): Array<{ turnId: string; operationId: string }>;
      notice(): string;
    };
  }
}

function Fixture() {
  const [source, setSource] = useState<SourceName>("completed");
  const [calls, setCalls] = useState<Array<{ turnId: string; operationId: string }>>([]);
  const [notice, setNotice] = useState("");
  const { t, setPref } = useI18n();
  const { records, verifiable, running } = SOURCES[source];

  // The same create-only binding the app shell clicks through: the host returns
  // an operation id, and every refusal is surfaced as the user's notice.
  const onFork = useCallback(async (target: ForkTargetView) => {
    setNotice("");
    try {
      const created = await app.CreateForkForTab("bench-fork-tab", target);
      setCalls((current) => [...current, { turnId: target.turnId, operationId: created.operationId ?? "" }]);
      if (created?.opened) {
        if (created.operationId) await app.AcknowledgeForkOperation("bench-fork-tab", created.operationId);
        return;
      }
      setNotice(created?.sessionId
        ? t("chat.branchRecoverChild", { session: created.sessionId })
        : t("chat.branchFailedDetail", { detail: created?.error ?? "" }));
    } catch (error) {
      setNotice(forkCreateFailureText(error));
    }
  }, [t]);

  useLayoutEffect(() => {
    window.forkFixture = {
      source: (name) => { setSource(name); setCalls([]); setNotice(""); },
      locale: (locale) => setPref(locale),
      calls: () => calls,
      notice: () => notice,
    };
  }, [calls, notice, setPref]);

  return <div style={{ height: "40vh", display: "flex", flexDirection: "column", background: "var(--bg)" }}>
    <Transcript items={itemsOf(records)} geometrySessionKey={`fixture-${source}`} running={running} tabId="bench-fork-tab"
      onPrompt={() => {}} onFork={(target) => void onFork(target)} forkTargets={targetsOf(source, records)} forkBlocked={null} />
    {notice && <div role="status" data-fork-notice style={{ flex: "none", padding: "4px 16px", color: "var(--text)" }}>{notice}</div>}
    <div data-fork-calls hidden>{calls.map((call, index) => <span key={index} data-fork-call data-turn={call.turnId}>{call.operationId}</span>)}</div>
  </div>;
}

createRoot(document.getElementById("root")!).render(<LocaleProvider><Fixture /></LocaleProvider>);
