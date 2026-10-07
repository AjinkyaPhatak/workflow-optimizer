"use client";

import { useReactFlow } from "@xyflow/react";
import { useCallback, useMemo } from "react";
import { describeIssues, type Issue } from "@/lib/workflow/issues";
import { useEditorStore } from "@/stores/workflow-editor/store";
import { useEditorContext } from "./EditorContext";

/** The backend's last validation result, worded for people. */
export function useIssues(): Issue[] {
  const { catalog } = useEditorContext();
  const validation = useEditorStore((s) => s.validation);
  const definition = useEditorStore((s) => s.definition);
  return useMemo(() => describeIssues(validation, definition, catalog), [validation, definition, catalog]);
}

/** Takes the user to a finding: selects its connection or node, brings it
 * into view and, for a configuration finding, focuses the field. Only the
 * selection and viewport change; the workflow itself is untouched. */
export function useFocusIssue() {
  const flow = useReactFlow();
  return useCallback(
    (issue: Issue) => {
      const { definition, select } = useEditorStore.getState();
      const edge = issue.edgeId ? definition.edges.find((e) => e.id === issue.edgeId) : undefined;
      if (edge) {
        select([], [edge.id]);
        void flow.fitView({ nodes: [{ id: edge.source }, { id: edge.target }], duration: 300, maxZoom: 1.2, padding: 0.4 });
        return;
      }
      const node = issue.nodeId ? definition.nodes.find((n) => n.id === issue.nodeId) : undefined;
      if (!node) return;
      select([node.id]);
      void flow.fitView({ nodes: [{ id: node.id }], duration: 300, maxZoom: 1.2, padding: 0.6 });
      if (issue.field) {
        // The configuration panel renders the field on the next frame.
        requestAnimationFrame(() => {
          const el = document.getElementById(`cfg-${issue.field}`);
          el?.scrollIntoView({ block: "nearest" });
          el?.focus({ preventScroll: true });
        });
      }
    },
    [flow],
  );
}
