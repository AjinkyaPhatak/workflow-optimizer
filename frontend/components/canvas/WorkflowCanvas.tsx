"use client";

import {
  Background,
  BackgroundVariant,
  ConnectionLineType,
  Controls,
  MarkerType,
  MiniMap,
  ReactFlow,
  getBezierPath,
  useReactFlow,
  type Connection,
  type ConnectionLineComponentProps,
  type Edge,
  type IsValidConnection,
  type NodeChange,
  type OnConnectStartParams,
} from "@xyflow/react";
import { useCallback, useEffect, useMemo, useRef, useState, type DragEvent, type MouseEvent as ReactMouseEvent } from "react";
import { NodeInfoCard } from "@/components/nodes/NodeInfoCard";
import { WorkflowNode } from "@/components/nodes/WorkflowNode";
import { Popover, type AnchorRect } from "@/components/ui/Popover";
import { useEditorContext } from "@/features/workflows/EditorContext";
import { useIssues } from "@/features/workflows/useIssues";
import { NODE_DRAG_TYPE, categoryClass } from "@/features/nodes/catalog";
import { issueEdgeIds } from "@/lib/workflow/issues";
import { toCanvas, type CanvasNode } from "@/lib/workflow/mapping";
import { checkConnection } from "@/lib/workflow/ports";
import { useEditorStore } from "@/stores/workflow-editor/store";
import type { NodeDefinition } from "@/types/api";
import { CanvasContext, type CanvasState, type ConnectingFrom } from "./CanvasContext";
import { EmptyCanvas } from "./EmptyCanvas";

const nodeTypes = { workflow: WorkflowNode };
export const GRID = 16;
/** Hover delay before node information appears (keeps passing the mouse over
 * the canvas quiet). */
const HOVER_DELAY_MS = 450;

const EDGE_COLOR = { normal: "#98a2b3", selected: "#3b5bdb", invalid: "#c92a2a" };

/** Green while the dragged edge would be accepted, red otherwise. */
function ConnectionLine({ fromX, fromY, toX, toY, fromPosition, toPosition, connectionStatus }: ConnectionLineComponentProps) {
  const [path] = getBezierPath({ sourceX: fromX, sourceY: fromY, sourcePosition: fromPosition, targetX: toX, targetY: toY, targetPosition: toPosition });
  const color = connectionStatus === "invalid" ? "var(--danger)" : connectionStatus === "valid" ? "var(--ok)" : "#868e96";
  return <path d={path} fill="none" stroke={color} strokeWidth={2} strokeDasharray={connectionStatus === "invalid" ? "5 4" : undefined} />;
}

function toAttempt(c: Connection | Edge) {
  return { source: c.source, sourcePort: c.sourceHandle ?? "", target: c.target, targetPort: c.targetHandle ?? "" };
}

type Hover = { kind: "node" | "edge"; id: string; anchor: AnchorRect };

function rectOf(el: Element): AnchorRect {
  const r = el.getBoundingClientRect();
  return { left: r.left, top: r.top, right: r.right, bottom: r.bottom };
}

export function WorkflowCanvas({ onAdd }: { onAdd: (def: NodeDefinition) => void }) {
  const { catalog } = useEditorContext();
  const flow = useReactFlow();
  const definition = useEditorStore((s) => s.definition);
  const selectedNodeIds = useEditorStore((s) => s.selectedNodeIds);
  const selectedEdgeIds = useEditorStore((s) => s.selectedEdgeIds);
  const readonly = useEditorStore((s) => s.mode === "readonly");
  const store = useEditorStore.getState;
  const issues = useIssues();
  // Fit the view to an existing workflow when it opens. An empty workflow
  // keeps the default viewport: fitting later, on its first node, would jump
  // the canvas under the user's next drop.
  const [fitOnOpen] = useState(() => useEditorStore.getState().definition.nodes.length > 0);
  const [hint, setHint] = useState<string | null>(null);
  const hintTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [connecting, setConnecting] = useState<ConnectingFrom | null>(null);
  // Rendered sizes are canvas-only state (never part of the definition).
  const [measured, setMeasured] = useState<Record<string, { width: number; height: number }>>({});

  // --- hover information ------------------------------------------------------
  const [hover, setHover] = useState<Hover | null>(null);
  const hoverTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  /** True while a mouse button is down (dragging, connecting, selecting). */
  const pressed = useRef(false);

  const hideHover = useCallback(() => {
    if (hoverTimer.current) clearTimeout(hoverTimer.current);
    hoverTimer.current = null;
    setHover(null);
  }, []);
  const scheduleHover = useCallback((h: Hover) => {
    if (hoverTimer.current) clearTimeout(hoverTimer.current);
    if (pressed.current) return;
    hoverTimer.current = setTimeout(() => setHover(h), HOVER_DELAY_MS);
  }, []);
  useEffect(() => {
    const up = () => {
      pressed.current = false;
    };
    window.addEventListener("pointerup", up);
    return () => {
      window.removeEventListener("pointerup", up);
      if (hoverTimer.current) clearTimeout(hoverTimer.current);
    };
  }, []);

  const flash = useCallback((text: string) => {
    setHint(text);
    if (hintTimer.current) clearTimeout(hintTimer.current);
    hintTimer.current = setTimeout(() => setHint(null), 2500);
  }, []);

  const edgeIssues = useMemo(() => issueEdgeIds(issues, definition), [issues, definition]);

  const { nodes, edges } = useMemo(() => {
    const c = toCanvas(definition, { selectedNodeIds: new Set(selectedNodeIds), selectedEdgeIds: new Set(selectedEdgeIds) });
    return {
      nodes: c.nodes.map((n) => (measured[n.id] ? { ...n, measured: measured[n.id] } : n)),
      edges: c.edges.map((e): Edge => {
        const invalid = edgeIssues.has(e.id);
        const color = invalid ? EDGE_COLOR.invalid : e.selected ? EDGE_COLOR.selected : EDGE_COLOR.normal;
        return {
          ...e,
          className: invalid ? "invalid" : undefined,
          markerEnd: { type: MarkerType.ArrowClosed, width: 16, height: 16, color },
          interactionWidth: 18,
        };
      }),
    };
  }, [definition, selectedNodeIds, selectedEdgeIds, measured, edgeIssues]);

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

  const onConnectStart = useCallback(
    (_: unknown, p: OnConnectStartParams) => {
      hideHover();
      if (p.nodeId && p.handleId && p.handleType) setConnecting({ nodeId: p.nodeId, port: p.handleId, handleType: p.handleType });
    },
    [hideHover],
  );

  /** Explains why a dropped connection was refused. */
  const onConnectEnd = useCallback(
    (_: MouseEvent | TouchEvent, state: { isValid: boolean | null; fromHandle: { nodeId: string; id?: string | null; type: string } | null; toHandle: { nodeId: string; id?: string | null; type: string } | null }) => {
      setConnecting(null);
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

  const canvasState = useMemo<CanvasState>(() => ({ issues, connecting }), [issues, connecting]);

  let hoverCard = null;
  if (hover?.kind === "node") {
    const n = definition.nodes.find((x) => x.id === hover.id);
    if (n) {
      hoverCard = (
        <Popover anchor={hover.anchor} testId="node-hover">
          <NodeInfoCard def={catalog.get(n.type)} title={n.name} issues={issues.filter((i) => i.nodeId === n.id && !i.edgeId)} />
        </Popover>
      );
    }
  } else if (hover?.kind === "edge") {
    const found = edgeIssues.get(hover.id);
    if (found) {
      hoverCard = (
        <Popover anchor={hover.anchor} testId="edge-hover">
          <div className="node-info">
            <div className="info-title">Invalid connection</div>
            {found.map((i) => (
              <p key={i.key} className="info-desc">⚠ {i.message}</p>
            ))}
          </div>
        </Popover>
      );
    }
  }

  return (
    <div
      className={`canvas-wrap ${connecting ? "is-connecting" : ""}`}
      onDragOver={onDragOver}
      onDrop={onDrop}
      onPointerDownCapture={() => {
        pressed.current = true;
        hideHover();
      }}
      data-testid="canvas"
    >
      {hint && <div className="canvas-hint error" role="status">{hint}</div>}
      {definition.nodes.length === 0 && <EmptyCanvas onAdd={onAdd} readonly={readonly} />}
      <CanvasContext.Provider value={canvasState}>
        <ReactFlow<CanvasNode>
          nodes={nodes}
          edges={edges}
          nodeTypes={nodeTypes}
          onNodesChange={onNodesChange}
          onEdgesChange={(changes) => store().applyEdgeChanges(changes)}
          onConnect={onConnect}
          onConnectStart={onConnectStart}
          onConnectEnd={onConnectEnd}
          isValidConnection={isValidConnection}
          connectionLineComponent={ConnectionLine}
          connectionLineType={ConnectionLineType.Bezier}
          onNodeMouseEnter={(e: ReactMouseEvent, n) => {
            if (e.buttons === 0) scheduleHover({ kind: "node", id: n.id, anchor: rectOf(e.currentTarget as Element) });
          }}
          onNodeMouseLeave={hideHover}
          onNodeDragStart={hideHover}
          onEdgeMouseEnter={(e: ReactMouseEvent, edge) => {
            if (e.buttons !== 0 || !edgeIssues.has(edge.id)) return;
            scheduleHover({ kind: "edge", id: edge.id, anchor: { left: e.clientX - 4, right: e.clientX + 4, top: e.clientY - 4, bottom: e.clientY + 4 } });
          }}
          onEdgeMouseLeave={hideHover}
          onMoveStart={hideHover}
          nodesDraggable={!readonly}
          nodesConnectable={!readonly}
          edgesReconnectable={false}
          deleteKeyCode={readonly ? null : ["Delete", "Backspace"]}
          multiSelectionKeyCode={["Shift", "Meta", "Control"]}
          selectionKeyCode="Shift"
          snapToGrid
          snapGrid={[GRID, GRID]}
          fitView={fitOnOpen}
          fitViewOptions={{ maxZoom: 1.2, padding: 0.2 }}
          minZoom={0.2}
          maxZoom={2}
          proOptions={{ hideAttribution: true }}
        >
          <Background variant={BackgroundVariant.Dots} gap={GRID} size={1} color="#cfd4dc" />
          <Controls showInteractive={false} />
          <MiniMap
            pannable
            zoomable
            nodeBorderRadius={6}
            nodeClassName={(n) => {
              const type = (n as CanvasNode).data.node.type;
              const bad = issues.some((i) => i.nodeId === n.id && i.severity === "error" && !i.edgeId);
              return `${categoryClass(catalog.get(type)?.category ?? "")} ${bad ? "invalid" : ""}`;
            }}
          />
        </ReactFlow>
      </CanvasContext.Provider>
      {hoverCard}
    </div>
  );
}
