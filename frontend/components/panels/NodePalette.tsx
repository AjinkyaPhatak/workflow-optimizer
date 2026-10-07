"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { NodeInfoCard } from "@/components/nodes/NodeInfoCard";
import { Popover, type AnchorRect } from "@/components/ui/Popover";
import { useEditorContext } from "@/features/workflows/EditorContext";
import { NODE_DRAG_TYPE, categoryClass, categoryLabel, groupByCategory, iconText } from "@/features/nodes/catalog";
import { useEditorStore } from "@/stores/workflow-editor/store";
import type { NodeDefinition } from "@/types/api";

const HOVER_DELAY_MS = 450;

/** The node palette, generated from GET /api/v1/nodes. Drag an item onto the
 * canvas, or double-click / Enter to add it at the center of the view.
 * Hovering an item shows what the node does. */
export function NodePalette({ onAdd }: { onAdd: (def: NodeDefinition) => void }) {
  const { catalogList } = useEditorContext();
  const readonly = useEditorStore((s) => s.mode === "readonly");
  const [filter, setFilter] = useState("");
  const [hover, setHover] = useState<{ def: NodeDefinition; anchor: AnchorRect } | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const groups = useMemo(() => {
    const f = filter.trim().toLowerCase();
    return groupByCategory(
      catalogList.filter((d) => !f || d.name.toLowerCase().includes(f) || d.type.includes(f) || d.description.toLowerCase().includes(f)),
    );
  }, [catalogList, filter]);

  const hide = () => {
    if (timer.current) clearTimeout(timer.current);
    setHover(null);
  };
  useEffect(() => () => {
    if (timer.current) clearTimeout(timer.current);
  }, []);

  return (
    <aside className="palette" aria-label="Node palette">
      <input
        type="search"
        placeholder="Search nodes"
        aria-label="Search nodes"
        data-palette-search
        value={filter}
        onChange={(e) => setFilter(e.target.value)}
      />
      {!readonly && <p className="palette-hint">Drag onto the canvas or double-click to add.</p>}
      {groups.map(([category, defs]) => (
        <section key={category}>
          <h3>{categoryLabel(category)}</h3>
          {defs.map((d) => (
            <div
              key={d.type}
              className="palette-item"
              data-testid={`palette-${d.type}`}
              draggable={!readonly}
              tabIndex={0}
              role="button"
              aria-label={`Add ${d.name} node`}
              onMouseEnter={(e) => {
                const r = e.currentTarget.getBoundingClientRect();
                if (timer.current) clearTimeout(timer.current);
                timer.current = setTimeout(() => setHover({ def: d, anchor: { left: r.left, right: r.right, top: r.top, bottom: r.bottom } }), HOVER_DELAY_MS);
              }}
              onMouseLeave={hide}
              onFocus={(e) => {
                // Keyboard focus only; a mouse press is about to drag.
                if (!e.currentTarget.matches(":focus-visible")) return;
                const r = e.currentTarget.getBoundingClientRect();
                setHover({ def: d, anchor: { left: r.left, right: r.right, top: r.top, bottom: r.bottom } });
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
              <span>{d.name}</span>
            </div>
          ))}
        </section>
      ))}
      {catalogList.length === 0 && <p className="muted">The node catalog is empty.</p>}
      {catalogList.length > 0 && groups.length === 0 && <p className="muted small">No nodes match “{filter}”.</p>}
      {hover && (
        <Popover anchor={hover.anchor} testId="palette-hover">
          <NodeInfoCard def={hover.def} />
        </Popover>
      )}
    </aside>
  );
}
