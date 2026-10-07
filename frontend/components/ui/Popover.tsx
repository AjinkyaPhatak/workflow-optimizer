"use client";

import { useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";

const GAP = 10;
const MARGIN = 8;

export interface AnchorRect {
  left: number;
  top: number;
  right: number;
  bottom: number;
}

/** Where a floating box of size w×h goes next to an anchor: to the right if it
 * fits, else to the left, else below; always clamped inside the viewport. */
export function placeBeside(anchor: AnchorRect, w: number, h: number, vw: number, vh: number): { left: number; top: number } {
  let left: number;
  let top = anchor.top;
  if (anchor.right + GAP + w <= vw - MARGIN) left = anchor.right + GAP;
  else if (anchor.left - GAP - w >= MARGIN) left = anchor.left - GAP - w;
  else {
    left = anchor.left;
    top = anchor.bottom + GAP;
    if (top + h > vh - MARGIN) top = anchor.top - GAP - h;
  }
  left = Math.min(Math.max(MARGIN, left), Math.max(MARGIN, vw - MARGIN - w));
  top = Math.min(Math.max(MARGIN, top), Math.max(MARGIN, vh - MARGIN - h));
  return { left, top };
}

/** A read-only floating card beside an anchor. It never takes pointer events,
 * so it cannot get in the way of dragging, connecting or selecting. */
export function Popover({ anchor, children, testId }: { anchor: AnchorRect; children: ReactNode; testId?: string }) {
  const ref = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState<{ left: number; top: number } | null>(null);

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    setPos(placeBeside(anchor, el.offsetWidth, el.offsetHeight, window.innerWidth, window.innerHeight));
  }, [anchor]);

  return createPortal(
    <div
      ref={ref}
      className="popover"
      role="tooltip"
      data-testid={testId}
      style={pos ? { left: pos.left, top: pos.top } : { left: -9999, top: -9999, visibility: "hidden" }}
    >
      {children}
    </div>,
    document.body,
  );
}
