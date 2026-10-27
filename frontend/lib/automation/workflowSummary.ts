import type { Workflow, WorkflowTemplate } from "@/types/api";
import type { AutomationTemplate, AutomationWorkflow, Step } from "@/types/automation";

export function workflowSummary(workflow: AutomationWorkflow): Workflow {
  return { ...workflow, project_id: workflow.project_id ?? "", description: null, active_version_id: workflow.active_version_id ?? null };
}

/** Presentation only. Execution always uses the original compiler plan. */
export function templateSummary(template: AutomationTemplate): WorkflowTemplate {
  const flatten = (steps: Step[]): Step[] => steps.flatMap(step => [step, ...flatten(step.children ?? []), ...flatten(step.otherwise ?? [])]);
  const nodes = flatten(template.plan.steps).map(step => ({
    id: step.id, name: step.name, type: step.kind === "tool" ? step.tool.replace(/^node\./, "") : step.kind,
    position: null,
    config: Object.fromEntries(step.inputs.filter(binding => binding.source === "literal" && binding.name.startsWith("config.")).map(binding => {
      let value: unknown = binding.value_json;
      try { value = JSON.parse(binding.value_json); } catch { /* Checked by backend on use. */ }
      return [binding.name.replace(/^config\./, ""), value];
    })),
  }));
  return { id: template.id, name: template.name, description: template.description, category: template.builtin ? template.category ?? "Workflows" : "Saved",
    setup: template.plan.questions.filter(question => question.required).map(question => question.prompt), saved: !template.builtin,
    node_types: [...new Set(nodes.map(node => node.type))], definition: { version: 1, nodes, edges: [], settings: {} }, automation: true };
}
