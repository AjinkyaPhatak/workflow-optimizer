"use client";
import { useId } from "react";
import type { Binding, Plan, Step, ToolDescriptor, ToolField } from "@/types/automation";
import { resultFields } from "@/lib/automation/editor";
import { fieldLabel } from "./PlanNode";

type EditorField = ToolField & { label?: string; multiline?: boolean; min?: number; max?: number };
const sourceLabels: Record<Binding["source"], string> = { literal: "Enter a value", step: "Use an earlier result", answer: "Ask me before running", template: "Text with missing details", item: "Current list item", event: "Trigger or event value" };
export function BindingEditor({ field, binding, steps, tools, plan, onChange, compact = false }: {
  field: EditorField; binding?: Binding; steps: Step[]; tools: ToolDescriptor[]; plan: Plan; onChange: (binding: Binding) => void; compact?: boolean;
}) {
  const id = useId();
  const base = binding ?? { name: field.name, source: "literal", ref: "", value_json: '""' };
  let parsed: unknown = "";
  try { parsed = JSON.parse(base.value_json || '""'); } catch { parsed = base.value_json; }
  const value = typeof parsed === "string" ? parsed : JSON.stringify(parsed);
  const choices = steps.flatMap(step => resultFields(step, tools).flatMap(output => {
    const result = [{ ref: step.id + "." + output.name, label: step.name + " → " + fieldLabel(output.name) }];
    if (output.name === "artifact" && output.type === "object") for (const key of ["id", "name", "content_type", "size"]) result.push({ ref: step.id + ".artifact." + key, label: step.name + " → File " + fieldLabel(key) });
    return result;
  }));
  const setValue = (text: string) => {
    let next: unknown = text;
    if (field.type === "number" && text.trim() && Number.isFinite(Number(text))) next = Number(text);
    if (["object", "array", "json"].includes(field.type)) { try { next = JSON.parse(text) as unknown; } catch { /* Preserve incomplete text. The checker validates it. */ } }
    onChange({ ...base, value_json: JSON.stringify(next) });
  };
  const input = base.source === "step" ? <select id={id} aria-label={"Result for " + field.name} value={base.ref} onChange={e => onChange({ ...base, ref: e.target.value })}>
    <option value="">Choose a result…</option>{base.ref && !choices.some(choice => choice.ref === base.ref) && <option value={base.ref}>Saved result — needs checking</option>}{choices.map(choice => <option key={choice.ref} value={choice.ref}>{choice.label}</option>)}
  </select> : base.source === "answer" ? <select id={id} aria-label={field.name} value={base.ref} onChange={e => onChange({ ...base, ref: e.target.value })}>
    <option value="">Choose a question…</option>{base.ref && !plan.questions.some(q => q.id === base.ref) && <option value={base.ref}>Saved question</option>}{plan.questions.map(q => <option key={q.id} value={q.id}>{q.prompt}</option>)}
  </select> : base.source === "item" || base.source === "event" ? <input id={id} aria-label={field.name} value={base.ref} placeholder="Leave empty for the whole value" onChange={e => onChange({ ...base, ref: e.target.value })} />
    : base.source === "literal" && field.type === "boolean" ? <select id={id} aria-label={field.name} value={parsed === true ? "true" : parsed === false ? "false" : ""} onChange={e => onChange({ ...base, value_json: e.target.value || '""' })}><option value="">Choose…</option><option value="true">Yes</option><option value="false">No</option></select>
    : base.source === "literal" && field.options?.length ? <select id={id} aria-label={field.name} value={String(parsed)} onChange={e => setValue(e.target.value)}><option value="">Choose…</option>{value && !field.options.some(option => option.value === String(parsed)) && <option value={String(parsed)}>Current value</option>}{field.options.map(option => <option key={option.value} value={option.value}>{option.label}</option>)}</select>
    : base.source === "template" || field.multiline || ["object", "array", "json"].includes(field.type) ? <textarea id={id} aria-label={field.name} rows={compact ? 2 : 3} value={value} placeholder={field.description} onChange={e => setValue(e.target.value)} />
    : <input id={id} aria-label={field.name} type={field.type === "number" ? "number" : "text"} min={field.min} max={field.max} value={value} placeholder={field.description} onChange={e => setValue(e.target.value)} />;
  return <div className={"field plan-binding " + (compact ? "compact" : "")}>
    <label htmlFor={id}>{field.label ?? fieldLabel(field.name)}{field.required ? " *" : ""}</label>
    {!compact && <select aria-label={"Source for " + field.name} value={base.source} onChange={e => {
      const source = e.target.value as Binding["source"];
      // Changing source is deliberate. Merely opening a field never changes it.
      onChange({ ...base, source, ref: source === base.source ? base.ref : "", value_json: source === base.source ? base.value_json : '""' });
    }}>{Object.entries(sourceLabels).map(([source, label]) => <option key={source} value={source}>{label}</option>)}</select>}
    {input}{!compact && field.description && <p className="hint">{field.description}</p>}
  </div>;
}
