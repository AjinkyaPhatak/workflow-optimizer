"use client";

import { useMemo, useState } from "react";
import { useEditorContext } from "@/features/workflows/EditorContext";
import { NODE_DRAG_TYPE, categoryClass, categoryLabel, groupByCategory, iconText } from "@/features/nodes/catalog";
import { useEditorStore } from "@/stores/workflow-editor/store";
import type { NodeDefinition } from "@/types/api";

/** The node palette, generated from GET /api/v1/nodes. Drag an item onto the
 * canvas, or double-click / Enter to add it at the center of the view. */
export function NodePalette({ onAdd }: { onAdd: (def: NodeDefinition) => void }) {
  const { catalogList } = useEditorContext();
  const readonly = useEditorStore((s) => s.mode === "readonly");
  const [filter, setFilter] = useState("");
  const groups = useMemo(() => {
    const f = filter.trim().toLowerCase();
    return groupByCategory(catalogList.filter((d) => !f || d.name.toLowerCase().includes(f) || d.type.includes(f)));
  }, [catalogList, filter]);

  return (
    <aside className="palette" aria-label="Node palette">
      <input placeholder="Search nodes" aria-label="Search nodes" value={filter} onChange={(e) => setFilter(e.target.value)} />
      {groups.map(([category, defs]) => (
        <section key={category}>
          <h3>{categoryLabel(category)}</h3>
          {defs.map((d) => (
            <div
              key={d.type}
              className="palette-item"
              data-testid={`palette-${d.type}`}
              title={d.description}
              draggable={!readonly}
              tabIndex={0}
              role="button"
              aria-label={`Add ${d.name} node`}
              onDragStart={(e) => {
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
    </aside>
  );
}
