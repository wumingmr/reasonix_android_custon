import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { CSSProperties, ReactNode, RefObject } from "react";
import { createPortal } from "react-dom";
import {
  elementLayoutSize,
  isElementExplicitlyHidden,
  MAX_INITIAL_OVERLAY_MEASUREMENT_FRAMES,
  validAnchorRect,
} from "../lib/anchoredOverlay";

type PopoverPosition = {
  left: number;
  top: number;
};
type PopoverPhase = "closed" | "open" | "closing";

const EDGE_GAP = 8;
const DEFAULT_OFFSET = 8;
export const ANCHORED_POPOVER_CLOSE_MS = 140;

function clamp(value: number, min: number, max: number): number {
  return Math.min(Math.max(value, min), max);
}

function samePosition(a: PopoverPosition | null, b: PopoverPosition): boolean {
  return !!a && Math.abs(a.left - b.left) < 0.5 && Math.abs(a.top - b.top) < 0.5;
}

function calculatePosition(
  anchor: DOMRect,
  menu: { width: number; height: number },
  align: "start" | "end",
  offset: number,
  placement: "auto" | "bottom",
): PopoverPosition {
  const viewportWidth = window.innerWidth;
  const viewportHeight = window.innerHeight;
  const preferredTop = anchor.top - menu.height - offset;
  const fallbackTop = anchor.bottom + offset;
  const top = placement === "bottom"
    ? Math.min(fallbackTop, Math.max(EDGE_GAP, viewportHeight - menu.height - EDGE_GAP))
    : preferredTop >= EDGE_GAP
    ? preferredTop
    : Math.min(fallbackTop, Math.max(EDGE_GAP, viewportHeight - menu.height - EDGE_GAP));
  const rawLeft = align === "end" ? anchor.right - menu.width : anchor.left;
  const left = clamp(rawLeft, EDGE_GAP, Math.max(EDGE_GAP, viewportWidth - menu.width - EDGE_GAP));
  return { left, top: clamp(top, EDGE_GAP, Math.max(EDGE_GAP, viewportHeight - menu.height - EDGE_GAP)) };
}

export function AnchoredPopover({
  open,
  anchorRef,
  onClose,
  className,
  children,
  align = "start",
  offset = DEFAULT_OFFSET,
  placement = "auto",
  style,
  closing = false,
}: {
  open: boolean;
  anchorRef: RefObject<HTMLElement | null>;
  onClose: () => void;
  className: string;
  children: ReactNode;
  align?: "start" | "end";
  offset?: number;
  placement?: "auto" | "bottom";
  style?: CSSProperties;
  closing?: boolean;
}) {
  const [phase, setPhase] = useState<PopoverPhase>(open ? "open" : "closed");
  const [position, setPosition] = useState<PopoverPosition | null>(null);
  const popoverRef = useRef<HTMLDivElement>(null);
  const phaseRef = useRef<PopoverPhase>(phase);
  const positionRef = useRef<PopoverPosition | null>(position);
  const onCloseRef = useRef(onClose);
  const invalidCloseNotifiedRef = useRef(false);
  onCloseRef.current = onClose;

  useLayoutEffect(() => {
    let id: number | undefined;
    if (open) {
      invalidCloseNotifiedRef.current = false;
      phaseRef.current = "open";
      setPhase("open");
      return undefined;
    }
    if (phaseRef.current === "closed") return undefined;
    phaseRef.current = "closing";
    setPhase("closing");
    const reduceMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    id = window.setTimeout(() => {
      phaseRef.current = "closed";
      setPhase("closed");
      positionRef.current = null;
      setPosition(null);
    }, reduceMotion ? 0 : ANCHORED_POPOVER_CLOSE_MS);
    return () => {
      if (id !== undefined) window.clearTimeout(id);
    };
  }, [open]);

  const rendered = closing || phase !== "closed";

  useLayoutEffect(() => {
    if (!rendered) {
      positionRef.current = null;
      setPosition(null);
      return;
    }
    if (!open) return;
    let frame: number | null = null;
    let initialMeasurementRetries = 0;
    const dismissInvalidAnchor = () => {
      positionRef.current = null;
      setPosition(null);
      phaseRef.current = "closed";
      setPhase("closed");
      if (!invalidCloseNotifiedRef.current) {
        invalidCloseNotifiedRef.current = true;
        onCloseRef.current();
      }
    };
    const updatePosition = () => {
      frame = null;
      const anchorElement = anchorRef.current;
      const anchor = validAnchorRect(anchorElement);
      const menuElement = popoverRef.current;
      const menu = menuElement ? elementLayoutSize(menuElement) : null;
      if (!anchor || !menu) {
        const explicitlyHidden = !!anchorElement && (
          !anchorElement.isConnected || isElementExplicitlyHidden(anchorElement)
        );
        if (positionRef.current === null && !explicitlyHidden && initialMeasurementRetries < MAX_INITIAL_OVERLAY_MEASUREMENT_FRAMES) {
          initialMeasurementRetries += 1;
          frame = window.requestAnimationFrame(updatePosition);
        } else {
          dismissInvalidAnchor();
        }
        return;
      }
      initialMeasurementRetries = 0;
      const next = calculatePosition(anchor, menu, align, offset, placement);
      if (!samePosition(positionRef.current, next)) {
        positionRef.current = next;
        setPosition(next);
      }
      frame = window.requestAnimationFrame(updatePosition);
    };
    updatePosition();

    return () => {
      if (frame !== null) window.cancelAnimationFrame(frame);
    };
  }, [rendered, open, anchorRef, align, offset, placement]);

  useEffect(() => {
    if (!open || closing) return;
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    const closeOnOutsideClick = (event: MouseEvent) => {
      const target = event.target;
      if (!(target instanceof Node)) return;
      if (popoverRef.current?.contains(target) || anchorRef.current?.contains(target)) return;
      onClose();
    };
    window.addEventListener("keydown", closeOnEscape);
    document.addEventListener("click", closeOnOutsideClick);
    return () => {
      window.removeEventListener("keydown", closeOnEscape);
      document.removeEventListener("click", closeOnOutsideClick);
    };
  }, [anchorRef, onClose, open]);

  if (!rendered) return null;

  return createPortal(
    <div
      ref={popoverRef}
      data-app-overlay=""
      data-anchored-popover="active"
      data-ready={position ? "true" : "false"}
      data-state={closing || phase === "closing" ? "closing" : "open"}
      aria-hidden={closing || phase === "closing" ? true : undefined}
      className={`anchored-popover ${className}`}
      style={{
        ...style,
        left: position?.left ?? 0,
        top: position?.top ?? 0,
        visibility: position ? "visible" : "hidden",
        pointerEvents: position ? undefined : "none",
      }}
      onMouseDown={(event) => {
        event.stopPropagation();
      }}
      onClick={(event) => {
        event.stopPropagation();
      }}
    >
      {children}
    </div>,
    document.body,
  );
}
