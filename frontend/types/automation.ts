export interface Binding { name: string; source: "literal" | "step" | "answer" | "template" | "item" | "event"; ref: string; value_json: string }
export interface Condition { left: Binding; op: string; right: Binding }
export interface Question {
  id: string; prompt: string; kind: "text" | "number" | "boolean" | "date" | "choice" | "contact" | "file";
  required: boolean; allow_other: boolean; options: { label: string; value: string }[] | null;
  example: string; min: number | null; max: number | null; step_id: string; input: string;
}
export interface Step {
  id: string; name: string; kind: string; tool: string; inputs: Binding[];
  children: Step[]; otherwise: Step[]; condition: Condition | null; question: Question | null;
  max_iterations: number; duration_seconds: number; workflow_ref: string;
}
export interface Budget { max_actions: number; max_tokens: number; max_active_seconds: number }
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
export interface ToolField { name: string; type: string; description: string; required: boolean; options?: { label: string; value: string }[] }
export interface ToolDescriptor { id: string; name: string; description: string; inputs: ToolField[]; outputs: ToolField[]; side_effects: string; provider?: string }
export interface Automation {
  source?: string;
  id?: string; workspace_id: string; user_id?: string; name: string; plan: Plan; credential_id?: string;
  cron: string; timezone: string; trigger: string; enabled: boolean; created_at?: string;
}
export interface RunEvent { id: number; run_id: string; kind: string; step_id: string; message: string; created_at: string }
export interface WhatsAppStatus { state: string; qr?: string }
