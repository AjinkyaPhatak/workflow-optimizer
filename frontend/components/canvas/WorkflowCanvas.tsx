"use client";

import {
  Background,
  BackgroundVariant,
  ConnectionLineType,
  Controls,
  MiniMap,
  ReactFlow,
  getBezierPath,
  useReactFlow,
  type Connection,
  type ConnectionLineComponentProps,
  type Edge,
  type IsValidConnection,
  type NodeChange,
} from "@xyflow/react";
import { useCallback, useMemo, useRef, useState, type DragEvent } from "react";
import { useEditorContext } from "@/features/workflows/EditorContext";
import { NODE_DRAG_TYPE } from "@/features/nodes/catalog";
import { WorkflowNode } from "@/components/nodes/WorkflowNode";
import { toCanvas, type CanvasNode } from "@/lib/workflow/mapping";
import { checkConnection } from "@/lib/workflow/ports";
import { useEditorStore } from "@/stores/workflow-editor/store";

const nodeTypes = { workflow: WorkflowNode };
const GRID = 16;

/** Green while the dragged edge would be accepted, red otherwise. */
function ConnectionLine({ fromX, fromY, toX, toY, fromPosition, toPosition, connectionStatus }: ConnectionLineComponentProps) {
  const [path] = getBezierPath({ sourceX: fromX, sourceY: fromY, sourcePosition: fromPosition, targetX: toX, targetY: toY, targetPosition: toPosition });
  const color = connectionStatus === "invalid" ? "var(--danger)" : connectionStatus === "valid" ? "var(--ok)" : "#868e96";
  return <path d={path} fill="none" stroke={color} strokeWidth={2} strokeDasharray={connectionStatus === "invalid" ? "5 4" : undefined} />;
}

function toAttempt(c: Connection | Edge) {
  return { source: c.source, sourcePort: c.sourceHandle ?? "", target: c.target, targetPort: c.targetHandle ?? "" };
}

export function WorkflowCanvas() {
  const { catalog } = useEditorContext();
  const flow = useReactFlow();
  const definition = useEditorStore((s) => s.definition);
  const selectedNodeIds = useEditorStore((s) => s.selectedNodeIds);
  const selectedEdgeIds = useEditorStore((s) => s.selectedEdgeIds);
  const readonly = useEditorStore((s) => s.mode === "readonly");
  const store = useEditorStore.getState;
  const [hint, setHint] = useState<string | null>(null);
  const hintTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  // Rendered sizes are canvas-only state (never part of the definition).
  const [measured, setMeasured] = useState<Record<string, { width: number; height: number }>>({});

  const flash = useCallback((text: string) => {
    setHint(text);
    if (hintTimer.current) clearTimeout(hintTimer.current);
    hintTimer.current = setTimeout(() => setHint(null), 2500);
  }, []);

  const { nodes, edges } = useMemo(() => {
    const c = toCanvas(definition, { selectedNodeIds: new Set(selectedNodeIds), selectedEdgeIds: new Set(selectedEdgeIds) });
    return { nodes: c.nodes.map((n) => (measured[n.id] ? { ...n, measured: measured[n.id] } : n)), edges: c.edges };
  }, [definition, selectedNodeIds, selectedEdgeIds, measured]);

  const onNodesChange = useCallback(
    (changes: NodeChange<CanvasNode>[]) => {
      const dims = changes.filter((c) => c.type === "dimensions" && c.dimensions);
      if (dims.length > 0) {
        setMeasured((m) => {
          const next = { ...m };
          for (const c of dims) if (c.type === "dimensions" && c.dimensions) next[c.id] = c.dimensions;
          return next;
        });
      }
      const rest = changes.filter((c) => c.type !== "dimensions");
      if (rest.length > 0) store().applyNodeChanges(rest);
    },
    [store],
  );

  const isValidConnection: IsValidConnection = useCallback(
    (c) => checkConnection(toAttempt(c), store().definition, catalog).ok,
    [catalog, store],
  );

  const onConnect = useCallback(
    (c: Connection) => {
      const res = store().connect(toAttempt(c), catalog);
      if (!res.ok) flash(res.reason);
    },
    [catalog, store, flash],
  );

  /** Explains why a dropped connection was refused. */
  const onConnectEnd = useCallback(
    (_: MouseEvent | TouchEvent, state: { isValid: boolean | null; fromHandle: { nodeId: string; id?: string | null; type: string } | null; toHandle: { nodeId: string; id?: string | null; type: string } | null }) => {
      if (state.isValid !== false || !state.fromHandle || !state.toHandle) return;
      const [from, to] = state.fromHandle.type === "source" ? [state.fromHandle, state.toHandle] : [state.toHandle, state.fromHandle];
      const res = checkConnection(
        { source: from.nodeId, sourcePort: from.id ?? "", target: to.nodeId, targetPort: to.id ?? "" },
        store().definition,
        catalog,
      );
      if (!res.ok) flash(res.reason);
    },
    [catalog, store, flash],
  );

  const onDragOver = useCallback((e: DragEvent) => {
    if (!e.dataTransfer.types.includes(NODE_DRAG_TYPE)) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = "copy";
  }, []);

  const onDrop = useCallback(
    (e: DragEvent) => {
      const type = e.dataTransfer.getData(NODE_DRAG_TYPE);
      const def = catalog.get(type);
      if (!def) return;
      e.preventDefault();
      const p = flow.screenToFlowPosition({ x: e.clientX, y: e.clientY });
      store().addNode(def, { x: Math.round(p.x / GRID) * GRID, y: Math.round(p.y / GRID) * GRID });
    },
    [catalog, flow, store],
  );

  return (
    <div className="canvas-wrap" onDragOver={onDragOver} onDrop={onDrop} data-testid="canvas">
      {hint && <div className="canvas-hint error" role="status">{hint}</div>}
      {definition.nodes.length === 0 && <div className="canvas-empty">Drag nodes here from the palette</div>}
      <ReactFlow<CanvasNode>
        nodes={nodes}
        edges={edges}
        nodeTypes={nodeTypes}
        onNodesChange={onNodesChange}
        onEdgesChange={(changes) => store().applyEdgeChanges(changes)}
        onConnect={onConnect}
        onConnectEnd={onConnectEnd}
        isValidConnection={isValidConnection}
        connectionLineComponent={ConnectionLine}
        connectionLineType={ConnectionLineType.Bezier}
        nodesDraggable={!readonly}
        nodesConnectable={!readonly}
        edgesReconnectable={false}
        deleteKeyCode={readonly ? null : ["Delete", "Backspace"]}
        multiSelectionKeyCode="Shift"
        selectionKeyCode="Shift"
        snapToGrid
        snapGrid={[GRID, GRID]}
        fitView
        fitViewOptions={{ maxZoom: 1.2, padding: 0.2 }}
        minZoom={0.2}
        maxZoom={2}
        proOptions={{ hideAttribution: true }}
      >
        <Background variant={BackgroundVariant.Dots} gap={GRID} size={1} />
        <Controls showInteractive={false} />
        <MiniMap pannable zoomable />
      </ReactFlow>
    </div>
  );
}
