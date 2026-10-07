"use client";

import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { NodeInfoCard } from "@/components/nodes/NodeInfoCard";
import { Popover, type AnchorRect } from "@/components/ui/Popover";
import { useEditorContext } from "@/features/workflows/EditorContext";
import { NODE_DRAG_TYPE, categoryClass, categoryLabel, groupByCategory, iconText, searchNodes } from "@/features/nodes/catalog";
import { useEditorStore } from "@/stores/workflow-editor/store";
import type { NodeDefinition } from "@/types/api";

const HOVER_DELAY_MS = 450;

function rectOf(el: Element): AnchorRect {
  const r = el.getBoundingClientRect();
  return { left: r.left, right: r.right, top: r.top, bottom: r.bottom };
}

/** The node palette, generated from GET /api/v1/nodes. Drag an item onto the
 * canvas, or double-click / Enter to add it at the center of the view.
 * Search matches names, descriptions, categories and ports; arrow keys move
 * through the results, Enter adds, Escape clears. Hovering (or moving to) an
 * item shows what the node does. */
export function NodePalette({ onAdd }: { onAdd: (def: NodeDefinition) => void }) {
  const { catalogList } = useEditorContext();
  const readonly = useEditorStore((s) => s.mode === "readonly");
  const [filter, setFilter] = useState("");
  const [active, setActive] = useState(0);
  const [hover, setHover] = useState<{ def: NodeDefinition; anchor: AnchorRect } | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const listRef = useRef<HTMLDivElement>(null);

  const searching = filter.trim() !== "";
  const results = useMemo(() => searchNodes(catalogList, filter), [catalogList, filter]);
  const groups = useMemo(
    () => (searching ? [["results", results] as [string, NodeDefinition[]]] : groupByCategory(catalogList)),
    [searching, results, catalogList],
  );
  const flat = groups.flatMap(([, defs]) => defs);
  const activeIndex = Math.min(active, Math.max(flat.length - 1, 0));

  const hide = () => {
    if (timer.current) clearTimeout(timer.current);
    setHover(null);
  };
  useEffect(() => () => {
    if (timer.current) clearTimeout(timer.current);
  }, []);

  /** Shows the info card next to the n-th visible item (keyboard). */
  const showFor = (n: number) => {
    const el = listRef.current?.querySelectorAll<HTMLElement>(".palette-item")[n];
    if (!el || !flat[n]) return;
    el.scrollIntoView?.({ block: "nearest" });
    setHover({ def: flat[n], anchor: rectOf(el) });
  };

  const onSearchKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (flat.length === 0) return;
      const next = (activeIndex + (e.key === "ArrowDown" ? 1 : flat.length - 1)) % flat.length;
      setActive(next);
      showFor(next);
    } else if (e.key === "Enter") {
      e.preventDefault();
      const def = searching ? flat[activeIndex] : undefined;
      if (def && !readonly) {
        onAdd(def);
        hide();
      }
    } else if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      hide();
      if (filter) setFilter("");
      else e.currentTarget.blur();
    }
  };

  const indexOf = new Map<string, number>();
  flat.forEach((d, i) => indexOf.set(d.type, i));

  return (
    <aside className="palette" aria-label="Node palette">
      <input
        type="search"
        placeholder="Search nodes…"
        aria-label="Search nodes"
        aria-controls="palette-results"
        aria-activedescendant={searching && flat[activeIndex] ? `palette-opt-${flat[activeIndex].type}` : undefined}
        data-palette-search
        value={filter}
        onChange={(e) => {
          setFilter(e.target.value);
          setActive(0);
          hide();
        }}
        onKeyDown={onSearchKey}
        onBlur={hide}
      />
      {!readonly && (
        <p className="palette-hint">{searching ? "↑ ↓ to choose, Enter to add, Esc to clear." : "Drag onto the canvas or double-click to add."}</p>
      )}
      <div ref={listRef} id="palette-results" role={searching ? "listbox" : undefined} aria-label={searching ? "Matching nodes" : undefined}>
        {searching && (
          <div className="palette-count" role="status" data-testid="palette-count">
            {results.length === 0 ? `No nodes match “${filter}”.` : `${results.length} node${results.length === 1 ? "" : "s"}`}
          </div>
        )}
        {groups.map(([category, defs]) => (
          <section key={category}>
            {!searching && <h3>{categoryLabel(category)}</h3>}
            {defs.map((d) => {
              const i = indexOf.get(d.type) ?? 0;
              return (
                <div
                  key={d.type}
                  id={`palette-opt-${d.type}`}
                  className={`palette-item ${searching && i === activeIndex ? "active" : ""}`}
                  data-testid={`palette-${d.type}`}
                  draggable={!readonly}
                  tabIndex={0}
                  role={searching ? "option" : "button"}
                  aria-selected={searching ? i === activeIndex : undefined}
                  aria-label={`Add ${d.name} node`}
                  onMouseEnter={(e) => {
                    const r = rectOf(e.currentTarget);
                    if (timer.current) clearTimeout(timer.current);
                    timer.current = setTimeout(() => setHover({ def: d, anchor: r }), HOVER_DELAY_MS);
                  }}
                  onMouseLeave={hide}
                  onFocus={(e) => {
                    // Keyboard focus only; a mouse press is about to drag.
                    if (!e.currentTarget.matches(":focus-visible")) return;
                    setHover({ def: d, anchor: rectOf(e.currentTarget) });
                  }}
                  onBlur={hide}
                  onDragStart={(e) => {
                    hide();
                    e.dataTransfer.setData(NODE_DRAG_TYPE, d.type);
                    e.dataTransfer.effectAllowed = "copy";
                  }}
                  onDoubleClick={() => !readonly && onAdd(d)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" && !readonly) onAdd(d);
                  }}
                >
                  <span className={`node-icon ${categoryClass(d.category)}`}>{iconText(d)}</span>
                  <span className="palette-name">{d.name}</span>
                  {searching && <span className="palette-cat">{categoryLabel(d.category)}</span>}
                </div>
              );
            })}
          </section>
        ))}
      </div>
      {catalogList.length === 0 && <p className="muted">The node catalog is empty.</p>}
      {hover && (
        <Popover anchor={hover.anchor} testId="palette-hover">
          <NodeInfoCard def={hover.def} />
        </Popover>
      )}
    </aside>
  );
}
