import { act } from "react";
import type { RemoteSessionApi } from "../../lib/useRemoteSession";
type Check = (value: boolean, label: string) => void;

export async function verifyRemoteSubmissionLifecycle(getProbe: () => RemoteSessionApi | undefined, setError: (error: Error | undefined) => void, tape: string[], flush: () => Promise<unknown>, ok: Check) {
  ok(Object.values(getProbe()?.transcript.localSubmissions ?? {}).find(submission => submission.text === "run tests")?.status === "accepted",
    "remote RPC success accepts the echo without removing it");
  const realNow = Date.now;
  try {
    Date.now = () => 123456789;
    await act(async () => { await Promise.all([getProbe()?.submit("same millisecond A"), getProbe()?.submit("same millisecond B")]); await flush(); });
  } finally { Date.now = realNow; }
  const sameTick = Object.values(getProbe()?.transcript.localSubmissions ?? {}).filter(submission => submission.text.startsWith("same millisecond"));
  ok(sameTick.length === 2 && new Set(sameTick.map(submission => submission.submissionId)).size === 2,
    "remote submissions within the same millisecond retain distinct identities");
  const duplicateBefore = tape.filter(entry => entry === "submit:tab-remote-2:duplicate remote").length;
  await act(async () => { await Promise.all([getProbe()?.submit("duplicate remote"), getProbe()?.submit("duplicate remote")]); await flush(); });
  ok(tape.filter(entry => entry === "submit:tab-remote-2:duplicate remote").length === duplicateBefore + 1,
    "a repeated in-flight remote submission dispatches once");
  setError(new Error("explicit rejection"));
  await act(async () => { await getProbe()?.submit("rejected remote").catch(() => {}); await flush(); });
  ok(Object.values(getProbe()?.transcript.localSubmissions ?? {}).find(submission => submission.text === "rejected remote")?.status === "failed",
    "remote explicit rejection preserves a failed echo");
  setError(new Error("network timeout"));
  await act(async () => { await getProbe()?.submit("unknown remote").catch(() => {}); await flush(); });
  ok(Object.values(getProbe()?.transcript.localSubmissions ?? {}).find(submission => submission.text === "unknown remote")?.status === "unknown",
    "remote transport timeout preserves an unknown echo");
  ok(tape.filter(entry => entry === "submit:tab-remote-2:unknown remote").length === 1,
    "unknown remote result is not automatically resubmitted");
  await act(async () => { await getProbe()?.submit("new remote while unknown").catch(() => {}); await flush(); });
  ok(!tape.includes("submit:tab-remote-2:new remote while unknown"),
    "a different remote message cannot bypass an unknown submission receipt");
  setError(undefined);
}

export async function verifyRemoteSubmissionTabIsolation(getProbe: () => RemoteSessionApi | undefined, render: (tabId: string) => void, flush: () => Promise<unknown>, ok: Check) {
  const retainedSubmissionIds = Object.keys(getProbe()?.transcript.localSubmissions ?? {});
  await act(async () => {
    render("tab-other-submissions");
    await flush();
  });
  ok(getProbe()?.transcript.localSubmissionOrder.length === 0, "remote tab hydration cannot copy another tab's pending echoes");
  await act(async () => {
    render("tab-remote-2");
    await flush();
  });
  ok(retainedSubmissionIds.length > 0 && retainedSubmissionIds.every(id => Boolean(getProbe()?.transcript.localSubmissions[id])),
    "switching back retains unresolved echoes in their original remote session");
}
