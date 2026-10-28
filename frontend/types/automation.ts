export interface Binding { name: string; source: "literal" | "step" | "answer" | "template" | "item" | "event"; ref: string; value_json: string }
export interface Condition { left: Binding; op: string; right: Binding }
export interface Question {
  purpose?: "" | "value" | "intent";
  context?: string; step_name?: string; field_label?: string;
  id: string; prompt: string; kind: "text" | "number" | "boolean" | "date" | "choice" | "contact" | "file";
  required: boolean; allow_other: boolean; options: { label: string; value: string }[] | null;
  example: string; min: number | null; max: number | null; step_id: string; input: string;
}
export interface Step {
  id: string; name: string; kind: string; tool: string; inputs: Binding[];
  children: Step[]; otherwise: Step[]; condition: Condition | null; question: Question | null;
  max_iterations: number; duration_seconds: number; workflow_ref: string;
}
export interface Budget { automatic?: boolean; max_actions: number; max_tokens: number; max_active_seconds: number }
export interface Plan { schema_version: 2; name: string; summary: string; mode: "once" | "continuous"; budget: Budget; steps: Step[]; questions: Question[] }
export interface Compilation { plan: Plan; ready: boolean; diagnostics: { step_id: string; code: string; message: string }[] }
export interface AssistantSession {
  id: string; workspace_id: string; user_id: string; revision: number; credential_id: string;
  messages: { role: string; text: string; created_at: string }[];
  compilation: Compilation | null; answers: Record<string, unknown>; created_at: string; updated_at: string;
}
export interface AutomationRun {
  id: string; workspace_id: string; session_id: string | null; revision: number; execution_backend: "temporal";
  plan: Plan; status: string; question: Question | null; error: string;
  outputs: Record<string, Record<string, unknown>>; created_at: string; updated_at: string;
}
export interface ToolField { name: string; type: string; description: string; required: boolean; options?: { label: string; value: string }[]; label?: string; default?: unknown; min?: number; max?: number; multiline?: boolean }
export interface ToolDescriptor { id: string; name: string; description: string; inputs: ToolField[]; outputs: ToolField[]; side_effects: string; provider?: string; category?: string; icon?: string; integration_id?: string }
export interface ControlDescriptor { kind: string; name: string; description: string; inputs: ToolField[]; outputs: ToolField[] }
export interface ConversionRule { from: string; to: string; tool: string; input: string; output: string }
export interface EditorLayout { revision: number; positions: Record<string, { x: number; y: number }> }
export interface AutomationWorkflow { id: string; workspace_id: string; project_id?: string | null; name: string; updated_at: string; created_at: string; active_version_id?: string | null }
export interface WorkflowEnvelope { workflow: AutomationWorkflow; session: AssistantSession; layout: EditorLayout }
export interface AutomationVersion { id: string; workflow_id: string; version_number: number; revision: number; plan: Plan; answers: Record<string, unknown>; credential_id: string; created_at: string; status: "PUBLISHED" }
export interface AutomationTemplate { id: string; name: string; description: string; plan: Plan; workspace_id?: string; builtin?: boolean; category?: string }
export interface BuildProgress { status: "idle" | "building" | "completed" | "failed" | "cancelled"; phase: "planning" | "checking" | "repairing" | "saving" | "complete"; pass: number; revision: number; request_id: string; plan?: Plan; updated_at: string }
export interface Automation {
  source?: string;
  id?: string; workspace_id: string; user_id?: string; name: string; plan: Plan; credential_id?: string;
  cron: string; timezone: string; trigger: string; enabled: boolean; created_at?: string;
}
export interface RunEvent { id: number; run_id: string; kind: string; step_id: string; message: string; created_at: string }
export interface WhatsAppStatus { state: string; qr?: string }
