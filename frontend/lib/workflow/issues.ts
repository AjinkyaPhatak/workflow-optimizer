// Presentation of backend validation results: what is wrong, where, and what
// to do about it. The backend graph validator decides WHAT is invalid; this
// module only rewords its findings (by error code) using the node catalog and
// the definition. It never checks the graph itself.

import type { NodeDefinition, ValidationError, ValidationResult, WorkflowDefinition } from "@/types/api";
import { labelOf } from "./labels";

export type IssueSeverity = "error" | "warning";

export interface Issue {
  key: string;
  severity: IssueSeverity;
  code: string;
  nodeId?: string;
  edgeId?: string;
  /** The configuration field the finding is about (INVALID_NODE_CONFIG). */
  field?: string;
  /** The input/output port the finding is about. */
  port?: string;
  /** The workflow variable the finding is about (INVALID_VARIABLE). */
  variable?: string;
  /** Where: a node name, a connection, or "Workflow". */
  title: string;
  /** What is wrong. */
  message: string;
  /** What to do about it. */
  hint?: string;
}

type Catalog = Map<string, NodeDefinition>;

function sentence(s: string): string {
  const t = s.trim();
  if (!t) return t;
  return t[0].toUpperCase() + t.slice(1) + (/[.!?]$/.test(t) ? "" : ".");
}

function namesWithRole(catalog: Catalog, role: string): string {
  const names = [...catalog.values()].filter((d) => d.role === role).map((d) => d.name);
  return names.length ? names.join(" or ") : role === "entry" ? "an entry node" : "an exit node";
}

function describe(e: ValidationError, severity: IssueSeverity, index: number, definition: WorkflowDefinition, catalog: Catalog): Issue {
  const node = e.node_id ? definition.nodes.find((n) => n.id === e.node_id) : undefined;
  const def = node ? catalog.get(node.type) : undefined;
  const edge = e.edge_id ? definition.edges.find((x) => x.id === e.edge_id) : undefined;
  const nameOf = (id: string) => definition.nodes.find((n) => n.id === id)?.name ?? id;

  const issue: Issue = {
    key: `${severity}-${index}-${e.code}`,
    severity,
    code: e.code,
    nodeId: node?.id,
    edgeId: edge?.id,
    port: e.port || undefined,
    title: node ? node.name : edge ? `${nameOf(edge.source)} → ${nameOf(edge.target)}` : "Workflow",
    message: sentence(e.message),
  };

  switch (e.code) {
    case "INVALID_NODE_CONFIG": {
      const name = e.port ?? "";
      const field = def?.config.find((f) => f.name === name);
      const label = labelOf(field ?? { name });
      issue.field = name || undefined;
      issue.port = undefined;
      if (/missing/.test(e.message)) {
        issue.message = `${label} is required.`;
        issue.hint = `Set ${label} in the node's configuration.`;
      } else if (/unknown/.test(e.message)) {
        issue.message = `"${name}" is not a setting of ${def?.name ?? "this node"}.`;
        issue.hint = "Remove it from the node's configuration.";
      } else if (/allowed options/.test(e.message)) {
        issue.message = `${label} must be one of the listed options.`;
        issue.hint = `Choose a ${label.toLowerCase()} from the list.`;
      } else if (/out of range/.test(e.message)) {
        const lo = field?.min, hi = field?.max;
        issue.message = lo !== undefined && hi !== undefined ? `${label} must be between ${lo} and ${hi}.` : lo !== undefined ? `${label} must be at least ${lo}.` : `${label} must be at most ${hi}.`;
        issue.hint = `Change the value of ${label}.`;
      } else if (/type/.test(e.message)) {
        issue.message = field ? `${label} must be a ${field.type} value.` : `${label} has the wrong type.`;
        issue.hint = `Change the value of ${label}.`;
      } else {
        issue.message = `${label}: ${sentence(e.message)}`;
      }
      break;
    }
    case "MISSING_REQUIRED_INPUT":
      issue.message = `Input "${e.port}" needs a connection.`;
      issue.hint = `Connect an output to the "${e.port}" input.`;
      break;
    case "MULTIPLE_CONNECTIONS_NOT_ALLOWED":
      issue.message = `Input "${e.port}" accepts only one connection.`;
      issue.hint = "Delete the extra connections.";
      break;
    case "INCOMPATIBLE_PORT_TYPES": {
      const s = edge && catalog.get(definition.nodes.find((n) => n.id === edge.source)?.type ?? "");
      const t = edge && catalog.get(definition.nodes.find((n) => n.id === edge.target)?.type ?? "");
      const out = edge && s?.outputs.find((p) => p.name === edge.source_port);
      const inp = edge && t?.inputs.find((p) => p.name === edge.target_port);
      if (edge) issue.title = `${nameOf(edge.source)} → ${nameOf(edge.target)}`;
      issue.message =
        edge && out && inp
          ? `Cannot connect ${out.type} output "${out.name}" to ${inp.type} input "${inp.name}".`
          : "The connected ports have different types.";
      issue.hint = "Connect ports of the same type (a json port accepts any type).";
      break;
    }
    case "SELF_REFERENCE":
      issue.message = "A node cannot connect to itself.";
      issue.hint = "Delete this connection.";
      break;
    case "DUPLICATE_EDGE":
      issue.message = "These ports are connected twice.";
      issue.hint = "Delete one of the connections.";
      break;
    case "INVALID_EDGE":
    case "INVALID_SOURCE_NODE":
    case "INVALID_TARGET_NODE":
    case "INVALID_SOURCE_PORT":
    case "INVALID_TARGET_PORT":
    case "INVALID_PORT_DIRECTION":
      issue.message = `This connection is broken: ${e.message}.`;
      issue.hint = "Delete it and connect the nodes again.";
      break;
    case "GRAPH_CYCLE":
      issue.message = "The workflow contains a loop.";
      issue.hint = "Remove a connection that leads back to an earlier node.";
      break;
    case "DISCONNECTED_NODE":
      if (/entry/.test(e.message)) {
        issue.message = "This node is not reachable from the start of the workflow.";
        issue.hint = `Connect it to the flow that starts at ${namesWithRole(catalog, "entry")}.`;
      } else {
        issue.message = "This node's result never reaches the end of the workflow.";
        issue.hint = `Connect it towards ${namesWithRole(catalog, "exit")}.`;
      }
      break;
    case "MISSING_INPUT_NODE":
      issue.message = "The workflow has no starting node.";
      issue.hint = `Add ${namesWithRole(catalog, "entry")}.`;
      break;
    case "MISSING_OUTPUT_NODE":
      issue.message = "The workflow has no final node.";
      issue.hint = `Add ${namesWithRole(catalog, "exit")}.`;
      break;
    case "INVALID_NODE_TYPE":
      issue.message = `Node type "${node?.type ?? ""}" is not available.`;
      issue.hint = "Delete this node.";
      break;
    case "INVALID_VARIABLE": {
      const name = typeof e.details?.variable === "string" ? e.details.variable : "";
      issue.variable = name;
      issue.title = "Workflow variables";
      issue.message = `${name ? `Variable "${name}": ` : ""}${sentence(e.message)}`;
      issue.hint = "Fix it in the Variables section of the workflow panel.";
      break;
    }
    case "INVALID_VARIABLE_REFERENCE":
      issue.message = "A {{variable}} reference is malformed.";
      issue.hint = "Check the {{…}} expressions in the configuration: use {{input.key}} or {{node_id.port}}.";
      break;
  }
  return issue;
}

/** Every finding of a validation result, errors first. */
export function describeIssues(result: ValidationResult | null, definition: WorkflowDefinition, catalog: Catalog): Issue[] {
  if (!result) return [];
  return [
    ...result.errors.map((e, i) => describe(e, "error", i, definition, catalog)),
    ...(result.warnings ?? []).map((e, i) => describe(e, "warning", i, definition, catalog)),
  ];
}

/** Edges a finding points at: by edge ID, or every edge into a port the
 * finding names (e.g. an input with too many connections). */
export function issueEdgeIds(issues: Issue[], definition: WorkflowDefinition): Map<string, Issue[]> {
  const out = new Map<string, Issue[]>();
  const add = (id: string, i: Issue) => out.set(id, [...(out.get(id) ?? []), i]);
  for (const i of issues) {
    if (i.edgeId) add(i.edgeId, i);
    else if (i.code === "MULTIPLE_CONNECTIONS_NOT_ALLOWED" && i.nodeId && i.port) {
      for (const e of definition.edges) if (e.target === i.nodeId && e.target_port === i.port) add(e.id, i);
    }
  }
  return out;
}

/** Findings about a node (including those about its configuration). */
export function nodeIssues(issues: Issue[], nodeId: string): Issue[] {
  return issues.filter((i) => i.nodeId === nodeId);
}
