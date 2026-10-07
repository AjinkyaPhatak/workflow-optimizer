// Editor state: the local working copy of the workflow definition and
// everything that exists only while editing (selection, history, dirty state,
// validation display). Server state (workflow, versions, node catalog,
// credentials, executions) lives in features/* and is not stored here.

import { applyEdgeChanges, applyNodeChanges, type EdgeChange, type NodeChange } from "@xyflow/react";
import { create } from "zustand";
import type { NodeDefinition, Position, ValidationResult, WorkflowDefinition, WorkflowEdge } from "@/types/api";
import { createNode, emptyDefinition, newEdgeId, normalizeDefinition, stableStringify } from "@/lib/workflow/definition";
import { fromCanvas, toCanvas, type CanvasNode } from "@/lib/workflow/mapping";
import { pasteFragment, type Fragment } from "@/lib/workflow/clipboard";
import { checkConnection, type ConnectionAttempt, type ConnectionCheck } from "@/lib/workflow/ports";

export const HISTORY_LIMIT = 100;

export type EditorMode = "editing" | "readonly";

export interface WorkflowEditorState {
  definition: WorkflowDefinition;
  /** Stable JSON of the last saved (or loaded) definition. */
  savedSnapshot: string;
  selectedNodeIds: string[];
  selectedEdgeIds: string[];
  validation: ValidationResult | null;
  mode: EditorMode;
  past: WorkflowDefinition[];
  future: WorkflowDefinition[];
  /** True while a node drag is in progress (one history entry per drag). */
  dragging: boolean;

  load: (definition: WorkflowDefinition | null, mode?: EditorMode) => void;
  markSaved: (definition?: WorkflowDefinition) => void;
  setValidation: (v: ValidationResult | null) => void;
  setMode: (mode: EditorMode) => void;

  addNode: (def: NodeDefinition, position: Position) => string | null;
  removeNodes: (ids: string[]) => void;
  moveNode: (id: string, position: Position) => void;
  renameNode: (id: string, name: string) => void;
  setConfigValue: (id: string, key: string, value: unknown) => void;
  connect: (c: ConnectionAttempt, catalog: Map<string, NodeDefinition>) => ConnectionCheck;
  removeEdges: (ids: string[]) => void;
  select: (nodeIds: string[], edgeIds?: string[]) => void;
  selectAll: () => void;
  /** Pastes a copied fragment as one undoable step and selects it. */
  paste: (fragment: Fragment) => string[];

  /** React Flow change events, applied through the canvas mapper. */
  applyNodeChanges: (changes: NodeChange<CanvasNode>[]) => void;
  applyEdgeChanges: (changes: EdgeChange[]) => void;

  undo: () => void;
  redo: () => void;
}

const initial = () => {
  const definition = emptyDefinition();
  return {
    definition,
    savedSnapshot: stableStringify(definition),
    selectedNodeIds: [] as string[],
    selectedEdgeIds: [] as string[],
    validation: null as ValidationResult | null,
    mode: "editing" as EditorMode,
    past: [] as WorkflowDefinition[],
    future: [] as WorkflowDefinition[],
    dragging: false,
  };
};

export const useEditorStore = create<WorkflowEditorState>((set, get) => {
  /** Applies an edit as one undoable step. */
  const commit = (next: WorkflowDefinition, extra: Partial<WorkflowEditorState> = {}) => {
    const { definition, past, mode } = get();
    if (mode === "readonly") return;
    set({ definition: next, past: [...past, definition].slice(-HISTORY_LIMIT), future: [], ...extra });
  };
  const editable = () => get().mode === "editing";

  return {
    ...initial(),

    load: (definition, mode = "editing") => {
      const d = normalizeDefinition(definition ?? emptyDefinition());
      set({ ...initial(), definition: d, savedSnapshot: stableStringify(d), mode });
    },
    markSaved: (definition) => {
      const d = definition ? normalizeDefinition(definition) : get().definition;
      set({ definition: d, savedSnapshot: stableStringify(d) });
    },
    setValidation: (validation) => set({ validation }),
    setMode: (mode) => set({ mode }),

    addNode: (def, position) => {
      if (!editable()) return null;
      const { definition } = get();
      const node = createNode(def, position, definition.nodes);
      commit({ ...definition, nodes: [...definition.nodes, node] }, { selectedNodeIds: [node.id], selectedEdgeIds: [] });
      return node.id;
    },

    removeNodes: (ids) => {
      if (!editable() || ids.length === 0) return;
      const gone = new Set(ids);
      const { definition, selectedNodeIds } = get();
      if (!definition.nodes.some((n) => gone.has(n.id))) return;
      commit(
        {
          ...definition,
          nodes: definition.nodes.filter((n) => !gone.has(n.id)),
          // A node's edges go with it.
          edges: definition.edges.filter((e) => !gone.has(e.source) && !gone.has(e.target)),
        },
        { selectedNodeIds: selectedNodeIds.filter((id) => !gone.has(id)) },
      );
    },

    moveNode: (id, position) => {
      const { definition } = get();
      commit({
        ...definition,
        nodes: definition.nodes.map((n) => (n.id === id ? { ...n, position: { x: position.x, y: position.y } } : n)),
      });
    },

    renameNode: (id, name) => {
      const { definition } = get();
      const node = definition.nodes.find((n) => n.id === id);
      if (!node || node.name === name) return;
      commit({ ...definition, nodes: definition.nodes.map((n) => (n.id === id ? { ...n, name } : n)) });
    },

    setConfigValue: (id, key, value) => {
      const { definition } = get();
      const node = definition.nodes.find((n) => n.id === id);
      if (!node) return;
      const config = { ...node.config };
      if (value === undefined) {
        if (!(key in config)) return;
        delete config[key];
      } else {
        if (stableStringify(config[key]) === stableStringify(value)) return;
        config[key] = value;
      }
      commit({ ...definition, nodes: definition.nodes.map((n) => (n.id === id ? { ...n, config } : n)) });
    },

    connect: (c, catalog) => {
      if (!editable()) return { ok: false, reason: "The editor is read-only" };
      const { definition } = get();
      const check = checkConnection(c, definition, catalog);
      if (!check.ok) return check;
      const edge: WorkflowEdge = {
        id: newEdgeId(definition.edges.map((e) => e.id)),
        source: c.source,
        source_port: c.sourcePort,
        target: c.target,
        target_port: c.targetPort,
      };
      commit({ ...definition, edges: [...definition.edges, edge] });
      return check;
    },

    removeEdges: (ids) => {
      if (!editable() || ids.length === 0) return;
      const gone = new Set(ids);
      const { definition, selectedEdgeIds } = get();
      if (!definition.edges.some((e) => gone.has(e.id))) return;
      commit(
        { ...definition, edges: definition.edges.filter((e) => !gone.has(e.id)) },
        { selectedEdgeIds: selectedEdgeIds.filter((id) => !gone.has(id)) },
      );
    },

    select: (nodeIds, edgeIds = []) => set({ selectedNodeIds: nodeIds, selectedEdgeIds: edgeIds }),
    selectAll: () => {
      const { definition } = get();
      set({ selectedNodeIds: definition.nodes.map((n) => n.id), selectedEdgeIds: definition.edges.map((e) => e.id) });
    },
    paste: (fragment) => {
      if (!editable() || fragment.nodes.length === 0) return [];
      const { definition, nodeIds } = pasteFragment(get().definition, fragment);
      commit(definition, { selectedNodeIds: nodeIds, selectedEdgeIds: [] });
      return nodeIds;
    },

    applyNodeChanges: (changes) => {
      const state = get();
      // Selection is editor state, not part of the definition.
      const selects = changes.filter((c) => c.type === "select");
      if (selects.length > 0) {
        const selected = new Set(state.selectedNodeIds);
        for (const c of selects) {
          if (c.type !== "select") continue;
          if (c.selected) selected.add(c.id);
          else selected.delete(c.id);
        }
        set({ selectedNodeIds: [...selected] });
      }
      if (!editable()) return;
      const removals = changes.filter((c) => c.type === "remove").map((c) => (c as { id: string }).id);
      if (removals.length > 0) {
        get().removeNodes(removals);
        return;
      }
      const moves = changes.filter((c) => c.type === "position" && c.position);
      if (moves.length === 0) {
        const ended = changes.some((c) => c.type === "position" && c.dragging === false);
        if (ended) set({ dragging: false });
        return;
      }
      const { definition, past, dragging } = get();
      const canvas = toCanvas(definition);
      const next = fromCanvas(applyNodeChanges(moves, canvas.nodes) as CanvasNode[], canvas.edges, definition);
      const isDrag = moves.some((c) => c.type === "position" && c.dragging);
      if (isDrag && dragging) {
        // Later steps of the same drag: no new history entry.
        set({ definition: next });
      } else {
        set({ definition: next, past: [...past, definition].slice(-HISTORY_LIMIT), future: [], dragging: isDrag });
      }
    },

    applyEdgeChanges: (changes) => {
      const state = get();
      const selects = changes.filter((c) => c.type === "select");
      if (selects.length > 0) {
        const selected = new Set(state.selectedEdgeIds);
        for (const c of selects) {
          if (c.type !== "select") continue;
          if (c.selected) selected.add(c.id);
          else selected.delete(c.id);
        }
        set({ selectedEdgeIds: [...selected] });
      }
      const removals = changes.filter((c) => c.type === "remove");
      if (removals.length === 0 || !editable()) return;
      const { definition } = get();
      const canvas = toCanvas(definition);
      const kept = applyEdgeChanges(removals, canvas.edges);
      commit(fromCanvas(canvas.nodes, kept, definition), {
        selectedEdgeIds: get().selectedEdgeIds.filter((id) => kept.some((e) => e.id === id)),
      });
    },

    undo: () => {
      const { past, future, definition, mode } = get();
      if (mode === "readonly" || past.length === 0) return;
      const prev = past[past.length - 1];
      set({ definition: prev, past: past.slice(0, -1), future: [definition, ...future], dragging: false, ...pruneSelection(prev) });
    },
    redo: () => {
      const { past, future, definition, mode } = get();
      if (mode === "readonly" || future.length === 0) return;
      const next = future[0];
      set({ definition: next, past: [...past, definition], future: future.slice(1), dragging: false, ...pruneSelection(next) });
    },
  };

  function pruneSelection(d: WorkflowDefinition) {
    const nodes = new Set(d.nodes.map((n) => n.id));
    const edges = new Set(d.edges.map((e) => e.id));
    return {
      selectedNodeIds: get().selectedNodeIds.filter((id) => nodes.has(id)),
      selectedEdgeIds: get().selectedEdgeIds.filter((id) => edges.has(id)),
    };
  }
});

/** Whether the working copy differs from the last saved definition. */
export function isDirty(s: Pick<WorkflowEditorState, "definition" | "savedSnapshot">): boolean {
  return stableStringify(s.definition) !== s.savedSnapshot;
}

export const useIsDirty = () => useEditorStore((s) => isDirty(s));
