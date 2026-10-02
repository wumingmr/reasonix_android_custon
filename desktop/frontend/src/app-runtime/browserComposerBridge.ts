import { browserElementDrafts } from "../lib/browserElementDrafts";
export function attachBrowserComposer(taskId: string, sessionId: string, deliver: (text: string) => void) {
  const key = JSON.stringify([taskId, sessionId]);
  const receive = () => { const draft = browserElementDrafts.getState().pending[key]; if (draft) { browserElementDrafts.getState().remove(key); deliver(draft.text); } };
  browserElementDrafts.setState({ target: { taskId, sessionId } });
  const off = browserElementDrafts.subscribe(receive); receive();
  return () => { off(); const target = browserElementDrafts.getState().target; if (target?.taskId === taskId && target.sessionId === sessionId) browserElementDrafts.setState({ target: undefined }); };
}
