// Types mirroring the Phase 12 REST API (internal/api/responses). They are the
// wire contract; the editor never invents fields the backend does not send.

export type UUID = string;

export type ValueType = "string" | "number" | "boolean" | "object" | "array" | "json" | "binary" | (string & {});

export interface ApiErrorBody {
  error: { code: string; message: string; details: unknown };
}

export interface User {
  id: UUID;
  email: string;
  name: string;
}

export type Role = "owner" | "admin" | "member" | "viewer";

export interface WorkspaceRef {
  id: UUID;
  name: string;
  role: Role;
}

export interface TokenResponse {
  access_token: string;
  token_type: "Bearer";
  expires_at: string;
  user: User;
  workspace?: WorkspaceRef;
}

export interface Me {
  user: User;
  workspaces: WorkspaceRef[];
}

export interface Project {
  id: UUID;
  workspace_id: UUID;
  name: string;
  description: string | null;
  created_at: string;
  updated_at: string;
}

export interface Workflow {
  id: UUID;
  project_id: UUID;
  name: string;
  description: string | null;
  active_version_id: UUID | null;
  created_at: string;
  updated_at: string;
}

export type VersionStatus = "DRAFT" | "PUBLISHED" | "ARCHIVED";

export interface VersionSummary {
  id: UUID;
  workflow_id: UUID;
  version_number: number;
  status: VersionStatus;
  created_by: UUID | null;
  created_at: string;
  published_at: string | null;
}

export interface Version extends VersionSummary {
  definition: WorkflowDefinition;
}

// --- workflow definition (the source of truth, internal/workflow) -----------

export interface Position {
  x: number;
  y: number;
}

export interface WorkflowNode {
  id: string;
  type: string;
  name: string;
  position: Position | null;
  config: Record<string, unknown>;
}

export interface WorkflowEdge {
  id: string;
  source: string;
  source_port: string;
  target: string;
  target_port: string;
}

export interface WorkflowDefinition {
  version: number;
  nodes: WorkflowNode[];
  edges: WorkflowEdge[];
  settings: Record<string, unknown>;
}

export interface ValidationError {
  code: string;
  message: string;
  node_id?: string;
  edge_id?: string;
  port?: string;
  details?: Record<string, unknown>;
}

export interface ValidationResult {
  valid: boolean;
  errors: ValidationError[];
  warnings: ValidationError[];
}

// --- node catalog (GET /nodes) ----------------------------------------------

export interface PortDefinition {
  name: string;
  type: ValueType;
  required: boolean;
  multiple: boolean;
  description: string;
}

export interface ConfigField {
  name: string;
  type: ValueType;
  required: boolean;
  default: unknown;
  description: string;
}

export interface NodeDefinition {
  type: string;
  name: string;
  category: string;
  description: string;
  role: "" | "entry" | "exit" | (string & {});
  side_effects: string;
  inputs: PortDefinition[];
  outputs: PortDefinition[];
  config: ConfigField[];
}

// --- executions --------------------------------------------------------------

export type ExecutionStatus = "PENDING" | "RUNNING" | "COMPLETED" | "FAILED" | "CANCELLED";

export interface ExecutionError {
  code: string;
  message: string;
  node_id?: string;
  retryable: boolean;
}

export interface Execution {
  id: UUID;
  workflow_id: UUID;
  workflow_version_id: UUID;
  status: ExecutionStatus;
  input: Record<string, unknown> | null;
  output: Record<string, unknown> | null;
  error: ExecutionError | null;
  attempt: number;
  max_attempts: number;
  cancel_requested: boolean;
  next_attempt_at: string | null;
  created_at: string;
  started_at: string | null;
  completed_at: string | null;
}

export interface NodeExecution {
  id: UUID;
  node_id: string;
  node_type: string;
  status: string;
  attempt: number;
  execution_attempt: number;
  started_at: string | null;
  completed_at: string | null;
  duration_ms: number | null;
  error: ExecutionError | null;
}

// --- observability (Phase 14) ------------------------------------------------

export interface TokenUsage {
  input_tokens: number;
  output_tokens: number;
  total_tokens: number;
}

/** GET /executions/{id}: the execution plus aggregates derived from its node
 * records. estimated_cost_usd is an estimate, not billing. */
export interface ExecutionDetails extends Execution {
  duration_ms: number | null;
  node_count: number;
  retries: number;
  usage: TokenUsage | null;
  estimated_cost_usd: number | null;
}

/** GET /executions/{id}/nodes item. input/output are redacted copies. */
export interface NodeExecutionDetails extends NodeExecution {
  execution_id: UUID;
  created_at: string;
  input?: Record<string, unknown>;
  output?: Record<string, unknown>;
  provider: string | null;
  model: string | null;
  usage: TokenUsage | null;
  estimated_cost_usd: number | null;
}

export type ExecutionEventType =
  | "EXECUTION_STARTED" | "EXECUTION_COMPLETED" | "EXECUTION_FAILED" | "EXECUTION_CANCELLED"
  | "NODE_STARTED" | "NODE_COMPLETED" | "NODE_FAILED" | "NODE_SKIPPED"
  | "RETRY_SCHEDULED" | "RETRY_STARTED";

export interface ExecutionEvent {
  id: UUID;
  execution_id: UUID;
  node_id: string | null;
  type: ExecutionEventType;
  timestamp: string;
  data: Record<string, unknown>;
}

export interface EventPage {
  events: ExecutionEvent[];
  page: number;
  page_size: number;
  total: number;
}

export interface ExecutionAccepted {
  execution_id: UUID;
  status: ExecutionStatus;
}

// --- credentials (metadata only: the API never returns secrets) -------------

export interface Credential {
  id: UUID;
  workspace_id: UUID;
  name: string;
  provider: string;
  credential_type: string;
  created_at: string;
  updated_at: string;
}

export interface List<T> {
  items: T[];
}

export interface Page<T> extends List<T> {
  page: number;
  page_size: number;
  total: number;
}

export const TERMINAL_STATUSES: ExecutionStatus[] = ["COMPLETED", "FAILED", "CANCELLED"];
