// Queue layout and drag mechanics adapted from ZCode (Apache-2.0).
// Reasonix retains the original queue identity and an independent edit draft.
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { closestCenter, DndContext, KeyboardSensor, PointerSensor, useSensor, useSensors, type Modifier } from "@dnd-kit/core";
import { SortableContext, sortableKeyboardCoordinates, verticalListSortingStrategy } from "@dnd-kit/sortable";
import { ChevronDown, ChevronUp, MoreHorizontal, Paperclip, Pause, Play, Square } from "lucide-react";
import { AnchoredPopover } from "./AnchoredPopover";
import type { PendingGuidance } from "./ComposerGuidanceShelf";
import type { InboxSnapshotLike } from "../lib/composerInboxQueue";
import { queueMoveAnchor, queuePending } from "../lib/inboxQueueCommands";
import { useInboxQueueEditor } from "../lib/useInboxQueueEditor";
import { inboxQueueCopy } from "../lib/inboxQueueCopy";
import { useI18n } from "../lib/i18n";
import { InboxQueueRow } from "./InboxQueueRow";
import { writeClipboardText } from "../lib/clipboard";
import "./ComposerInboxQueue.css";

// Same constrained vertical drag as ZCode; preserve dimensions while moving.
const restrictToQueue: Modifier = ({ transform, draggingNodeRect, activeNodeRect, containerNodeRect, windowRect }) => {
  const node = draggingNodeRect ?? activeNodeRect, boundary = containerNodeRect ?? windowRect;
  return { ...transform, x: 0, y: node && boundary ? Math.min(Math.max(transform.y, boundary.top - node.top), boundary.bottom - node.bottom) : transform.y };
};

export function ComposerInboxQueue({ scope, tabId, sessionPath, items, snapshot, disabled, running, turnId, onSnapshot, onRefresh, onEditingChange, onRestoreDraft, onStop, stopDisabled }: {
  scope: string; tabId: string; sessionPath: string; items: PendingGuidance[]; snapshot?: InboxSnapshotLike;
  disabled: boolean; running: boolean; onSnapshot: (snapshot: InboxSnapshotLike) => unknown;
  onRefresh: () => void; onEditingChange: (active: boolean) => void; turnId?: string;
  onRestoreDraft: (text: string) => void; onStop?: () => void; stopDisabled?: boolean;
}) {
  const { locale } = useI18n();
  const copy = inboxQueueCopy(locale);
  const editor = useInboxQueueEditor(scope, tabId, sessionPath, onSnapshot, onRefresh);
  const [expanded, setExpanded] = useState(false);
  const [menu, setMenu] = useState(false);
  const menuAnchor = useRef<HTMLButtonElement>(null);
  const [dragError, setDragError] = useState(false);
  const [copied, setCopied] = useState(false);
  const dragRevision = useRef<number | undefined>(undefined);
  const editInput = useRef<HTMLTextAreaElement>(null);
  const queueRoot = useRef<HTMLElement>(null);
  const wasEditing = useRef(false);
  const sensors = useSensors(useSensor(PointerSensor, { activationConstraint: { distance: 6 } }), useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }));
  const pending = items.filter(item => queuePending(item.state));
  const accepted = items.filter(item => item.state === "steer_accepted");
  const ids = pending.map(item => item.id);
  const draft = editor.open ? editor.drafts.edit : undefined;
  // Editing preserves the user's disclosure choice; a restored draft must
  // remain reachable even when it belongs to a normally collapsed row.
  const visible = expanded ? pending : pending.filter((item, index) => index < 2 || item.id === draft?.id);
  const locked = disabled || Boolean(snapshot?.readonly) || snapshot?.mutationsSupported !== true || editor.busy;
  const currentItem = draft && snapshot?.items?.find(item => item.id === draft.id);
  const departed = Boolean(draft && snapshot && (!currentItem || !queuePending(currentItem.state)));
  const reason = draft?.reason || (departed ? "item_not_pending" : "");
  const notice = dragError ? "order_changed" : editor.notice === reason ? "" : editor.notice;
  const message = (key: string) => copy[key as keyof typeof copy] || copy.error;
  useLayoutEffect(() => { onEditingChange(Boolean(draft)); return () => onEditingChange(false); }, [Boolean(draft), onEditingChange]);
  useEffect(() => { if (draft && !editor.busy) editInput.current?.focus(); }, [draft?.id, editor.busy, departed]);
  useEffect(() => {
    const restoreFocus = wasEditing.current && !draft;
    wasEditing.current = Boolean(draft);
    if (!restoreFocus) return;
    const composer = queueRoot.current?.closest(".composer-wrap")?.querySelector<HTMLElement>("#composer-input");
    const frame = requestAnimationFrame(() => { if (composer?.isConnected) composer.focus({ preventScroll: true }); });
    return () => cancelAnimationFrame(frame);
  }, [Boolean(draft)]);
  const move = (id: string, before: string | null) => {
    setDragError(false);
    void editor.mutate({ kind: "move", itemId: id, beforeItemId: before, queueRevision: snapshot?.revision });
  };
  const editForm = draft && <div className="inbox-queue__editor"
    onKeyDown={event => {
        if (event.nativeEvent.isComposing || event.keyCode === 229) return;
        if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); editor.close(); }
        if (event.key === "Enter" && (event.ctrlKey || event.metaKey)) { event.preventDefault(); event.stopPropagation(); if (!locked && !reason) void editor.save(); }
      }}>
    <textarea ref={editInput} rows={3} value={draft.value} aria-label={copy.editing} disabled={editor.busy} onChange={event => editor.update(event.target.value)} />
    {!!draft.references.length && <div className="inbox-queue__references">{draft.references.map((ref, i) => <span key={`${ref}:${i}`}><Paperclip size={13} />{ref}</span>)}</div>}
    {reason && <p className="inbox-queue__notice inbox-queue__notice--warning" role="alert">{message(reason)}</p>}
    <footer>
      <button type="button" className="btn btn--small" disabled={editor.busy} onClick={editor.close}>{copy.cancel}</button>
      {reason ? <><button type="button" className="btn btn--small" disabled={editor.busy} onClick={() => void editor.reload()}>{copy.reload}</button><button type="button" className="btn btn--primary" disabled={editor.busy} onClick={() => { const value = editor.recover(); if (value !== undefined) onRestoreDraft(value); }}>{copy.recover}</button></>
        : <button type="button" className="btn btn--primary" disabled={locked || !draft.value.trim()} title={copy.shortcut} onClick={() => void editor.save()}>{copy.save}</button>}
    </footer>
  </div>;
  if (!items.length && !editor.drafts.edit && !editor.drafts.recovered.length) return null;
  return <>
    {draft && running && <div className="inbox-queue__running"><span>{copy.currentRunning}</span>{onStop && <button type="button" className="btn btn--small" disabled={stopDisabled} onClick={onStop}><Square size={13} />{copy.stop}</button>}</div>}
    <section ref={queueRoot} className={`inbox-queue${draft ? " inbox-queue--editing" : ""}`} aria-label={copy.title}>
    {accepted.length > 0 && <div className="inbox-queue__accepted">{copy.inFlight} · {accepted.map(item => item.text).join(" · ")}</div>}
    <header className="inbox-queue__head">
      {pending.length > 2 && <button type="button" className="inbox-queue__icon" onClick={() => setExpanded(!expanded)} aria-label={expanded ? copy.collapse : copy.expand} aria-expanded={expanded}>{expanded ? <ChevronUp size={15} /> : <ChevronDown size={15} />}</button>}
      <strong title={copy.after}>{copy.title} <span>{pending.length}</span></strong>
      {snapshot?.paused && <span className="inbox-queue__hint">{copy.paused}</span>}
      {snapshot?.paused && <button type="button" className="btn btn--small" disabled={locked} onClick={() => void editor.mutate({ kind: "pause", paused: false })}><Play size={13} />{copy.resume}</button>}
      {pending.length > 0 && <div className="inbox-queue__more" onKeyDown={event => { if (event.key === "Escape") { setMenu(false); event.stopPropagation(); } }}>
        <button ref={menuAnchor} type="button" className="inbox-queue__icon" aria-label={copy.queueMore} aria-expanded={menu} disabled={locked} onClick={() => setMenu(!menu)}><MoreHorizontal size={16} /></button>
        <AnchoredPopover open={menu} anchorRef={menuAnchor} onClose={() => setMenu(false)} className="inbox-queue__menu" align="end">
          <button type="button" disabled={locked} onClick={() => { setMenu(false); void editor.mutate({ kind: "pause", paused: !snapshot?.paused }); }}>{snapshot?.paused ? <Play size={13} /> : <Pause size={13} />}{snapshot?.paused ? copy.resume : copy.pause}</button>
        </AnchoredPopover>
      </div>}
    </header>
    {snapshot?.mutationsSupported === false && <p className="inbox-queue__notice">{copy.unsupported}</p>}
    {snapshot?.readonly && <p className="inbox-queue__notice">{copy.read_only}</p>}
    <DndContext sensors={sensors} collisionDetection={closestCenter} modifiers={[restrictToQueue]}
      onDragStart={() => { dragRevision.current = snapshot?.revision; setExpanded(true); }}
      onDragCancel={() => { dragRevision.current = undefined; }}
      onDragEnd={({ active, over }) => {
        if (dragRevision.current !== snapshot?.revision) { setDragError(true); onRefresh(); return; }
        const before = over && queueMoveAnchor(ids, String(active.id), String(over.id));
        if (over && before !== undefined) move(String(active.id), before);
        dragRevision.current = undefined;
      }}>
      <SortableContext items={ids} strategy={verticalListSortingStrategy}>
        <ol className="inbox-queue__list">{visible.map(item => <InboxQueueRow key={item.id} item={item} index={ids.indexOf(item.id)} ids={ids} copy={copy}
          locked={locked} editing={draft?.id === item.id} onEdit={() => void editor.edit(item.id)} onMove={before => move(item.id, before)}
          onDelete={() => void editor.mutate({ kind: "delete", itemId: item.id })} onRetry={() => void editor.mutate({ kind: "retry", itemId: item.id })}
          onGuide={running && turnId ? () => void editor.mutate({ kind: "steer", itemId: item.id, turnId }) : undefined}>{draft?.id === item.id ? editForm : null}</InboxQueueRow>)}</ol>
      </SortableContext>
    </DndContext>
    {notice && <p className="inbox-queue__notice" role="status">{message(notice)}</p>}
    {/* A dispatch may remove the row while it is being edited. Keep its input
        reachable as a recovery surface, never disguise it as a pending row. */}
    {draft && !ids.includes(draft.id) && <div className="inbox-queue__departed">{editForm}</div>}
    {!draft && editor.drafts.edit && <button type="button" className="btn btn--small" disabled={editor.busy} onClick={editor.show}>{copy.editing} · {copy.retained}</button>}
    {!!editor.drafts.recovered.length && <details className="inbox-queue__drafts"><summary>{copy.recovered} ({editor.drafts.recovered.length})</summary>{editor.drafts.recovered.map((saved, i) => <div key={i}><span>{saved.value.slice(0, 100)}</span><button type="button" className="btn btn--small" disabled={editor.busy} onClick={() => { editor.close(); onRestoreDraft(saved.value); }}>{copy.restore}</button><button type="button" className="btn btn--small" onClick={() => void writeClipboardText(saved.value).then(setCopied)}>{copy.copy}</button></div>)}{copied && <span role="status">{copy.copied}</span>}</details>}
  </section></>;
}
