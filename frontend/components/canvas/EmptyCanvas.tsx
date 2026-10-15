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
            <ol className="empty-steps">
              <li><b>Add steps</b> from the bar above: an Input, an AI step, an app like Gmail.</li>
              <li><b>Fill in</b> each step right on its card. Type <code>{"{{name}}"}</code> to leave a blank another step fills in.</li>
              <li><b>Connect</b> the steps by dragging from a dot on one card to a dot on the next, then run it.</li>
            </ol>
            <div className="row center">
              {entries.map((d) => (
                <button key={d.type} className="primary" onClick={() => onAdd(d)}>
                  + Start with an {d.name}
                </button>
              ))}
              <button onClick={() => document.querySelector<HTMLInputElement>("[data-palette-search]")?.focus()}>Search for a step</button>
            </div>
          </>
        )}
      </div>
    </div>
  );
}
