import type { AssistantSession, Automation, AutomationRun, Budget, Plan, ToolDescriptor, WhatsAppStatus } from "@/types/automation";
import { binaryPost, request, stream } from "./client";

const sessionPath = (id: string) => "/assistant/sessions/" + encodeURIComponent(id);
const runPath = (id: string) => "/automation-runs/" + encodeURIComponent(id);
export const assistantApi = {
  importLegacy: (workflow: string, version: string) => request<AssistantSession>("POST", "/assistant/import", { body: { workflow_id: workflow, version_id: version } }),
  tools: () => request<{ items: ToolDescriptor[]; comparisons: { label: string; value: string }[] }>("GET", "/assistant/tools"),
  sessions: (workspace: string) => request<{ items: AssistantSession[] }>("GET", "/assistant/sessions", { query: { workspace_id: workspace } }),
  create: (workspace: string, credential?: string) => request<AssistantSession>("POST", "/assistant/sessions", { body: { workspace_id: workspace, ...(credential ? { credential_id: credential } : {}) } }),
  get: (id: string) => request<AssistantSession>("GET", sessionPath(id)),
  message: (id: string, revision: number, text: string, signal?: AbortSignal) => request<AssistantSession>("POST", sessionPath(id) + "/messages", { body: { revision, text }, signal }),
  edit: (id: string, revision: number, plan: Plan) => request<AssistantSession>("PUT", sessionPath(id) + "/plan", { body: { revision, plan } }),
  repair: (id: string, revision: number, signal?: AbortSignal) => request<AssistantSession>("POST", sessionPath(id) + "/repair", { body: { revision }, signal }),
  answer: (id: string, revision: number, question: string, value: unknown) => request<AssistantSession>("POST", sessionPath(id) + "/answers", { body: { revision, question_id: question, value } }),
  start: (id: string, revision: number, fresh = false) => request<AutomationRun>("POST", sessionPath(id) + "/runs", { body: { revision, ...(fresh ? { request_id: crypto.randomUUID() } : {}) } }),
  runs: (workspace: string) => request<{ items: AutomationRun[] }>("GET", "/automation-runs", { query: { workspace_id: workspace } }),
  run: (id: string) => request<AutomationRun>("GET", runPath(id)),
  answerRun: (run: AutomationRun, question: string, value: unknown) => request<void>("POST", runPath(run.id) + "/answers", { body: { revision: run.revision, question_id: question, value } }),
  control: (id: string, action: "pause" | "resume" | "cancel", budget?: Budget) => request<void>("POST", runPath(id) + "/control", { body: { action, budget } }),
  event: (id: string, event: unknown) => request<void>("POST", runPath(id) + "/events", { body: { event } }),
  stream: (id: string, after: string, onEvent: (kind: string, data: unknown, id: string) => void, signal: AbortSignal) => stream(runPath(id) + "/events?after=" + encodeURIComponent(after), onEvent, signal),
  automations: (workspace: string) => request<{ items: Automation[] }>("GET", "/automations", { query: { workspace_id: workspace } }),
  saveAutomation: (body: Automation) => request<Automation>("PUT", "/automations", { body }),
  deleteAutomation: (id: string) => request<void>("DELETE", "/automations/" + encodeURIComponent(id)),
  voiceToken: (workspace: string, signal?: AbortSignal) => request<{ token: string }>("POST", "/voice/token", { body: { workspace_id: workspace }, signal }),
  speak: (workspace: string, text: string, signal?: AbortSignal) => binaryPost("/voice/speak", { workspace_id: workspace, text }, signal),
  whatsApp: (workspace: string) => request<WhatsAppStatus>("GET", "/whatsapp", { query: { workspace_id: workspace } }),
  connectWhatsApp: (workspace: string) => request<WhatsAppStatus>("POST", "/whatsapp/connect", { body: { workspace_id: workspace } }),
  disconnectWhatsApp: (workspace: string) => request<void>("POST", "/whatsapp/disconnect", { body: { workspace_id: workspace } }),
};
