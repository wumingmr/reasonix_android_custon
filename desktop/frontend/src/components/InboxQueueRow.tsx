// Adapted from zai-org/ZCode ConversationQueuePanel.tsx at 872ad960.
// Apache-2.0; see licenses/ZCode.txt. Changed: Reasonix controls, keyboard
// sorting, explicit retry, and edit-in-place rather than withdrawal.
import { useRef, useState, type ReactNode } from "react";
import { AnchoredPopover } from "./AnchoredPopover";
import { useSortable } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { GripVertical, Pencil, MoreHorizontal, CornerDownRight } from "lucide-react";
import type { PendingGuidance } from "./ComposerGuidanceShelf";
import { queueActionAnchor } from "../lib/inboxQueueCommands";
import { inboxQueueCopy } from "../lib/inboxQueueCopy";

export function InboxQueueRow({ item, index, ids, locked, editing, children, copy, onEdit, onMove, onDelete, onRetry, onGuide }: {
  item: PendingGuidance; index: number; ids: string[]; locked: boolean; editing: boolean;
  copy: ReturnType<typeof inboxQueueCopy>; onEdit: () => void; onMove: (before: string | null) => void;
  onDelete: () => void; onRetry: () => void; onGuide?: () => void;
  children?: ReactNode;
}) {
  const [menu, setMenu] = useState(false);
  const menuAnchor = useRef<HTMLButtonElement>(null);
  const { attributes, listeners, isDragging, setNodeRef, setActivatorNodeRef, transform, transition } = useSortable({ id: item.id, disabled: locked || editing });
  return <li ref={setNodeRef} className={`inbox-queue__row${editing ? " inbox-queue__row--editing" : ""}`} data-item-id={item.id}
    style={{ transform: CSS.Transform.toString(transform ? { ...transform, scaleX: 1, scaleY: 1 } : null), transition, zIndex: isDragging ? 1 : undefined }}>
    <button type="button" ref={setActivatorNodeRef} className="inbox-queue__handle" disabled={locked || editing} aria-label={`${copy.drag} ${index + 1}`} {...attributes} {...listeners}><GripVertical size={16} /></button>
    {editing ? children : <>
    <span className="inbox-queue__ordinal">{index + 1}</span>
    <span className="inbox-queue__body" title={item.text}><span>{item.text}</span>
      {item.state !== "queued" && <small>{item.state === "uncertain" ? copy.uncertain : item.blockReason || copy.blocked}</small>}
    </span>
    {onGuide && <button type="button" className="inbox-queue__icon" disabled={locked || item.state !== "queued" || item.paused} onClick={onGuide} aria-label={copy.guide} title={copy.guide}><CornerDownRight size={15} /></button>}
    <button type="button" className="inbox-queue__icon" disabled={locked} onClick={onEdit} aria-label={copy.edit} title={copy.edit}><Pencil size={15} /></button>
    <div className="inbox-queue__more" onKeyDown={event => { if (event.key === "Escape") { setMenu(false); event.stopPropagation(); } }}>
      <button ref={menuAnchor} type="button" className="inbox-queue__icon" disabled={locked} aria-label={copy.more} aria-expanded={menu} onClick={() => setMenu(!menu)}><MoreHorizontal size={16} /></button>
      <AnchoredPopover open={menu} anchorRef={menuAnchor} onClose={() => setMenu(false)} className="inbox-queue__menu" align="end">
        {(["first", "up", "down", "last"] as const).map(action => {
          const before = queueActionAnchor(ids, item.id, action);
          return <button type="button" key={action} disabled={locked || before === undefined} onClick={() => { if (before !== undefined) onMove(before); setMenu(false); }}>{copy[action]}</button>;
        })}
        {(item.state === "uncertain" || item.state === "blocked") && <button type="button" disabled={locked} onClick={() => { onRetry(); setMenu(false); }}>{copy.retry}</button>}
        <button type="button" disabled={locked} onClick={() => { onDelete(); setMenu(false); }}>{copy.remove}</button>
      </AnchoredPopover>
    </div>
    </>}
  </li>;
}
