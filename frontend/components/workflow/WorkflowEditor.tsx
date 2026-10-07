"use client";

import { ReactFlowProvider, useReactFlow } from "@xyflow/react";
import { useCallback, useEffect, useRef, useState } from "react";
import { WorkflowCanvas } from "@/components/canvas/WorkflowCanvas";
import { BottomPanel, type BottomTab } from "@/components/panels/BottomPanel";
import { ConfigPanel } from "@/components/panels/ConfigPanel";
import { NodePalette } from "@/components/panels/NodePalette";
import { ErrorBanner } from "@/components/ui/ErrorBanner";
import { EditorContext } from "@/features/workflows/EditorContext";
import { useEditorSession } from "@/features/workflows/useEditorSession";
import { isDirty, useEditorStore } from "@/stores/workflow-editor/store";
import type { NodeDefinition } from "@/types/api";
import { EditorHeader } from "./EditorHeader";

function isTyping(target: EventTarget | null): boolean {
  const el = target as HTMLElement | null;
  return !!el && (el.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(el.tagName));
}

function EditorLayout({ workflowId }: { workflowId: string }) {
  const session = useEditorSession(workflowId);
  const flow = useReactFlow();
  const centerRef = useRef<HTMLDivElement>(null);
  const [tab, setTab] = useState<BottomTab>("validation");
  const validation = useEditorStore((s) => s.validation);

  // Show validation findings as soon as they arrive.
  const [shownValidation, setShownValidation] = useState(validation);
  if (validation !== shownValidation) {
    setShownValidation(validation);
    if (validation) setTab("validation");
  }

  // Keyboard: undo / redo / save (inputs keep their native undo).
  const { save } = session;
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!(e.ctrlKey || e.metaKey) || isTyping(e.target)) return;
      const k = e.key.toLowerCase();
      if (k === "z" && !e.shiftKey) {
        e.preventDefault();
        useEditorStore.getState().undo();
      } else if ((k === "z" && e.shiftKey) || k === "y") {
        e.preventDefault();
        useEditorStore.getState().redo();
      } else if (k === "s") {
        e.preventDefault();
        if (isDirty(useEditorStore.getState()) && useEditorStore.getState().mode === "editing") void save();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [save]);

  // Warn before leaving with unsaved changes.
  useEffect(() => {
    const onUnload = (e: BeforeUnloadEvent) => {
      if (isDirty(useEditorStore.getState())) e.preventDefault();
    };
    window.addEventListener("beforeunload", onUnload);
    return () => window.removeEventListener("beforeunload", onUnload);
  }, []);

  const addAtCenter = useCallback(
    (def: NodeDefinition) => {
      const r = centerRef.current?.getBoundingClientRect();
      const p = r ? flow.screenToFlowPosition({ x: r.left + r.width / 2, y: r.top + r.height / 3 }) : { x: 0, y: 0 };
      const n = useEditorStore.getState().definition.nodes.length;
      useEditorStore.getState().addNode(def, { x: p.x + (n % 5) * 24, y: p.y + (n % 5) * 24 });
    },
    [flow],
  );

  if (session.loading) return <div className="page-loading">Loading workflow…</div>;
  if (session.loadError || !session.workflow) {
    return (
      <div className="dashboard">
        <ErrorBanner error={session.loadError ?? new Error("Workflow not found")} />
      </div>
    );
  }

  return (
    <EditorContext.Provider value={session}>
      <div className="editor">
        <EditorHeader onExecute={() => setTab("run")} />
        <div className="editor-body">
          <NodePalette onAdd={addAtCenter} />
          <div className="editor-center" ref={centerRef}>
            <WorkflowCanvas />
            <BottomPanel tab={tab} onTab={setTab} workflowId={workflowId} />
          </div>
          <ConfigPanel />
        </div>
      </div>
    </EditorContext.Provider>
  );
}

export function WorkflowEditor({ workflowId }: { workflowId: string }) {
  return (
    <ReactFlowProvider>
      <EditorLayout workflowId={workflowId} />
    </ReactFlowProvider>
  );
}
