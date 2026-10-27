import type { ReactNode } from "react";

/** Shared visual shell; legacy and compiler editors supply their own state. */
export function WorkflowNodeFrame({ id, type, kind, selected, dragging, invalid, warning, children }: {
  id: string; type: string; kind: string; selected?: boolean; dragging?: boolean;
  invalid?: boolean; warning?: boolean; children: ReactNode;
}) {
  return <div className={`wf-node kind-${kind} ${selected ? "selected" : ""} ${invalid ? "invalid" : warning ? "warning" : ""} ${dragging ? "dragging" : ""}`} data-testid={`node-${id}`} data-node-type={type}>{children}</div>;
}
