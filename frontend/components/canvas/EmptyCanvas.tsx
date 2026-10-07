"use client";

import { useEditorContext } from "@/features/workflows/EditorContext";
import type { NodeDefinition } from "@/types/api";

/** The starting state of a workflow without nodes. Entry nodes (role
 * "entry" in the catalog) are offered as the natural first step. */
export function EmptyCanvas({ onAdd, readonly }: { onAdd: (def: NodeDefinition) => void; readonly: boolean }) {
  const { catalogList } = useEditorContext();
  const entries = catalogList.filter((d) => d.role === "entry");

  return (
    <div className="canvas-empty" data-testid="canvas-empty">
      <div className="empty-card">
        <div className="empty-mark" aria-hidden>
          <span />
          <span />
          <span />
        </div>
        <h2>{readonly ? "This workflow is empty" : "Build your workflow"}</h2>
        {!readonly && (
          <>
            <p className="muted">Drag a node from the palette onto the canvas, or start with an input.</p>
            <div className="row center">
              {entries.map((d) => (
                <button key={d.type} className="primary" onClick={() => onAdd(d)}>
                  + Add {d.name} node
                </button>
              ))}
              <button onClick={() => document.querySelector<HTMLInputElement>("[data-palette-search]")?.focus()}>Browse nodes</button>
            </div>
          </>
        )}
      </div>
    </div>
  );
}
