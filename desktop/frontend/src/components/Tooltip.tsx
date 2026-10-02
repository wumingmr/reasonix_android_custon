import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import type { CSSProperties, KeyboardEvent as ReactKeyboardEvent, ReactNode } from "react";
import { createPortal } from "react-dom";
import {
  elementLayoutSize,
  isElementExplicitlyHidden,
  MAX_INITIAL_OVERLAY_MEASUREMENT_FRAMES,
  validAnchorRect,
} from "../lib/anchoredOverlay";

type TooltipSide = "top" | "bottom" | "left" | "right";
type TooltipPosition = { left: number; top: number; side: TooltipSide; arrowX: number; arrowY: number };

const GAP = 8;
const EDGE_PAD = 8;
const ARROW_SIZE = 7;
const ARROW_PAD = 12;

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}

function oppositeSide(side: TooltipSide): TooltipSide {
  if (side === "top") return "bottom";
  if (side === "bottom") return "top";
  if (side === "left") return "right";
  return "left";
}

function samePosition(
  current: TooltipPosition | null,
  next: TooltipPosition,
): boolean {
  return !!current && (
    current.side === next.side &&
    Math.abs(current.left - next.left) < 0.5 &&
    Math.abs(current.top - next.top) < 0.5 &&
    Math.abs(current.arrowX - next.arrowX) < 0.5 &&
    Math.abs(current.arrowY - next.arrowY) < 0.5
  );
}

export function Tooltip({
  label,
  children,
  side = "top",
  fill = false,
  block = false,
  disabled = false,
  className,
  delay = 180,
}: {
  label?: ReactNode;
  children: ReactNode;
  side?: TooltipSide;
  fill?: boolean;
  block?: boolean;
  disabled?: boolean;
  className?: string;
  delay?: number;
}) {
  const id = useId();
  const triggerRef = useRef<HTMLElement | null>(null);
  const tooltipRef = useRef<HTMLDivElement>(null);
  const showTimerRef = useRef<number | null>(null);
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState<TooltipPosition | null>(null);
  const positionRef = useRef<TooltipPosition | null>(null);
  const active = !disabled && label !== undefined && label !== null && label !== "";

  const clearTimer = useCallback(() => {
    if (showTimerRef.current === null) return;
    window.clearTimeout(showTimerRef.current);
    showTimerRef.current = null;
  }, []);

  const show = (delay = 180) => {
    if (!active) return;
    clearTimer();
    showTimerRef.current = window.setTimeout(() => {
      showTimerRef.current = null;
      if (validAnchorRect(triggerRef.current)) setOpen(true);
    }, delay);
  };

  const hide = useCallback(() => {
    clearTimer();
    setOpen(false);
    positionRef.current = null;
    setPosition(null);
  }, [clearTimer]);

  useLayoutEffect(() => {
    if (!open || !active) return;
    let frame: number | null = null;
    let measured = false;
    let initialMeasurementRetries = 0;
    const updatePosition = () => {
      frame = null;
      const trigger = triggerRef.current;
      const rect = validAnchorRect(trigger);
      const tip = tooltipRef.current;
      const tipSize = tip ? elementLayoutSize(tip) : null;
      if (!rect || !tipSize) {
        const explicitlyHidden = !!trigger && (!trigger.isConnected || isElementExplicitlyHidden(trigger));
        if (!measured && !explicitlyHidden && initialMeasurementRetries < MAX_INITIAL_OVERLAY_MEASUREMENT_FRAMES) {
          initialMeasurementRetries += 1;
          frame = window.requestAnimationFrame(updatePosition);
        } else {
          hide();
        }
        return;
      }
      measured = true;
      initialMeasurementRetries = 0;
      const space = {
        top: rect.top - EDGE_PAD,
        bottom: window.innerHeight - rect.bottom - EDGE_PAD,
        left: rect.left - EDGE_PAD,
        right: window.innerWidth - rect.right - EDGE_PAD,
      };
      let actualSide = side;
      if ((side === "top" || side === "bottom") && space[side] < tipSize.height + GAP + ARROW_SIZE) {
        const opposite = oppositeSide(side);
        if (space[opposite] > space[side]) actualSide = opposite;
      } else if ((side === "left" || side === "right") && space[side] < tipSize.width + GAP + ARROW_SIZE) {
        const opposite = oppositeSide(side);
        if (space[opposite] > space[side]) actualSide = opposite;
      }

      let left =
        actualSide === "left"
          ? rect.left - tipSize.width - GAP - ARROW_SIZE
          : actualSide === "right"
            ? rect.right + GAP + ARROW_SIZE
            : rect.left + rect.width / 2 - tipSize.width / 2;
      let top =
        actualSide === "top"
          ? rect.top - tipSize.height - GAP - ARROW_SIZE
          : actualSide === "bottom"
            ? rect.bottom + GAP + ARROW_SIZE
            : rect.top + rect.height / 2 - tipSize.height / 2;

      left = clamp(left, EDGE_PAD, window.innerWidth - tipSize.width - EDGE_PAD);
      top = clamp(top, EDGE_PAD, window.innerHeight - tipSize.height - EDGE_PAD);
      const arrowX = clamp(rect.left + rect.width / 2 - left, ARROW_PAD, tipSize.width - ARROW_PAD);
      const arrowY = clamp(rect.top + rect.height / 2 - top, ARROW_PAD, tipSize.height - ARROW_PAD);

      const next = { left, top, side: actualSide, arrowX, arrowY };
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
  }, [active, hide, open, side]);

  useEffect(() => {
    if (!active) hide();
    return () => clearTimer();
  }, [active, clearTimer, hide]);

  const triggerClass = `tooltip-trigger${fill ? " tooltip-trigger--fill" : ""}${block ? " tooltip-trigger--block" : ""}${className ? ` ${className}` : ""}`;
  const setTriggerRef = (node: HTMLElement | null) => {
    triggerRef.current = node;
  };
  const triggerProps = {
    className: triggerClass,
    "aria-describedby": open ? id : undefined,
    onMouseEnter: () => show(delay),
    onMouseLeave: hide,
    onPointerDownCapture: hide,
    onFocus: () => show(0),
    onBlur: hide,
    onKeyDown: (event: ReactKeyboardEvent<HTMLElement>) => {
      if (event.key === "Escape" || event.key === "Enter" || event.key === " ") hide();
    },
  };

  return (
    <>
      {block ? <div ref={setTriggerRef} {...triggerProps}>{children}</div> : <span ref={setTriggerRef} {...triggerProps}>{children}</span>}
      {open &&
        active &&
        createPortal(
          <div
            id={id}
            ref={tooltipRef}
            className={`tooltip tooltip--${position?.side ?? side}`}
            role="tooltip"
            style={{
              left: position?.left ?? 0,
              top: position?.top ?? 0,
              visibility: position ? "visible" : "hidden",
              pointerEvents: position ? undefined : "none",
              "--tooltip-arrow-x": `${position?.arrowX ?? 0}px`,
              "--tooltip-arrow-y": `${position?.arrowY ?? 0}px`,
            } as CSSProperties}
          >
            {label}
          </div>,
          document.body,
        )}
    </>
  );
}
