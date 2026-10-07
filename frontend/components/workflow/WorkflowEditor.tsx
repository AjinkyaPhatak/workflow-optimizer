"use client";

import { ReactFlowProvider, useReactFlow } from "@xyflow/react";
import { useCallback, useEffect, useRef, useState } from "react";
import { GRID, WorkflowCanvas } from "@/components/canvas/WorkflowCanvas";
import { BottomPanel, type BottomTab } from "@/components/panels/BottomPanel";
import { ConfigPanel } from "@/components/panels/ConfigPanel";
import { NodePalette } from "@/components/panels/NodePalette";
import { ErrorBanner } from "@/components/ui/ErrorBanner";
import { EditorContext } from "@/features/workflows/EditorContext";
import { useEditorSession } from "@/features/workflows/useEditorSession";
import { copyFragment, type Fragment } from "@/lib/workflow/clipboard";
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

  // Keyboard: undo / redo / save / select all / copy / paste / escape.
  // Text fields keep their native behaviour; Delete and Backspace are
  // handled by the canvas.
  const { save } = session;
  const clipboard = useRef<Fragment | null>(null);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (isTyping(e.target)) return;
      const editor = useEditorStore.getState();
      if (e.key === "Escape") {
        if (editor.selectedNodeIds.length || editor.selectedEdgeIds.length) editor.select([], []);
        return;
      }
      if (!(e.ctrlKey || e.metaKey) || e.altKey) return;
      const k = e.key.toLowerCase();
      if (k === "a") {
        e.preventDefault();
        editor.selectAll();
      } else if (k === "c") {
        const fragment = copyFragment(editor.definition, editor.selectedNodeIds);
        if (fragment) {
          e.preventDefault();
          clipboard.current = fragment;
        }
      } else if (k === "v") {
        if (clipboard.current && editor.mode === "editing") {
          e.preventDefault();
          editor.paste(clipboard.current);
          // Pasting again offsets from the previous copy.
          clipboard.current = {
            ...clipboard.current,
            nodes: clipboard.current.nodes.map((n) => ({ ...n, position: n.position && { x: n.position.x + 32, y: n.position.y + 32 } })),
          };
        }
      } else if (k === "z" && !e.shiftKey) {
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
      // Step right (then down) past existing nodes instead of stacking on them.
      const taken = useEditorStore.getState().definition.nodes.map((n) => n.position).filter((q) => q !== null);
      const snap = (v: number) => Math.round(v / GRID) * GRID;
      let pos = { x: snap(p.x - 110), y: snap(p.y) };
      for (let i = 0; i < 50 && taken.some((q) => Math.abs(q.x - pos.x) < 300 && Math.abs(q.y - pos.y) < 140); i++) {
        pos = i % 4 === 3 ? { x: snap(p.x - 110), y: pos.y + 176 } : { x: pos.x + 336, y: pos.y };
      }
      useEditorStore.getState().addNode(def, pos);
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
            <WorkflowCanvas onAdd={addAtCenter} />
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
