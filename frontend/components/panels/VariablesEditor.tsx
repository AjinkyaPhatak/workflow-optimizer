"use client";

import { useState } from "react";
import { useIssues } from "@/features/workflows/useIssues";
import { stableStringify } from "@/lib/workflow/definition";
import { useEditorStore } from "@/stores/workflow-editor/store";
import type { VariableType, WorkflowVariable } from "@/types/api";

const TYPES: VariableType[] = ["string", "number", "boolean", "object", "array"];

/** The default's text form in the editor ("" for none). */
function defaultText(v: WorkflowVariable): string {
  if (v.default === null || v.default === undefined) return "";
  return typeof v.default === "string" ? v.default : JSON.stringify(v.default);
}

/** Parses the default text for the type; undefined when it does not parse.
 * (The backend checks the result; this only converts the text field.) */
function parseDefault(type: VariableType, text: string): unknown {
  if (type === "string") return text;
  if (text.trim() === "") return undefined;
  if (type === "number") return Number.isNaN(Number(text)) ? undefined : Number(text);
  if (type === "boolean") return text === "true" ? true : text === "false" ? false : undefined;
  try {
    return JSON.parse(text);
  } catch {
    return undefined;
  }
}

function VariableRow({ index, variable, onChange, onRemove, readonly, problems }: {
  index: number; variable: WorkflowVariable; onChange: (v: WorkflowVariable) => void; onRemove: () => void; readonly: boolean; problems: string[];
}) {
  const [name, setName] = useState(variable.name);
  const [description, setDescription] = useState(variable.description ?? "");
  const [text, setText] = useState(defaultText(variable));
  const required = variable.default === null || variable.default === undefined;
  const [parseError, setParseError] = useState<string | null>(null);

  const commitDefault = (t: string, type = variable.type, req = required) => {
    if (req) {
      setParseError(null);
      onChange({ ...variable, type, default: null });
      return;
    }
    const value = parseDefault(type, t);
    if (value === undefined) {
      setParseError(`Enter a ${type} value, or mark the variable as required.`);
      return;
    }
    setParseError(null);
    onChange({ ...variable, type, default: value });
  };

  const id = (part: string) => `var-${index}-${part}`;
  return (
    <li className={`variable-row ${problems.length ? "has-error" : ""}`} data-testid={`variable-${index}`}>
      <div className="variable-head">
        <code className="variable-ref">{`{{${variable.name || "name"}}}`}</code>
        {!readonly && <button type="button" className="small-button danger" onClick={onRemove} aria-label={`Remove variable ${variable.name}`}>Remove</button>}
      </div>
      <div className="variable-grid">
        <label htmlFor={id("name")}>Name</label>
        <input id={id("name")} value={name} disabled={readonly} placeholder="customer_name" autoComplete="off" spellCheck={false}
          onChange={(e) => setName(e.target.value)}
          onBlur={() => name !== variable.name && onChange({ ...variable, name: name.trim() })} />
        <label htmlFor={id("type")}>Type</label>
        <select id={id("type")} value={variable.type} disabled={readonly}
          onChange={(e) => {
            const type = e.target.value as VariableType;
            if (required) onChange({ ...variable, type, default: null });
            else {
              const value = parseDefault(type, text);
              onChange({ ...variable, type, default: value === undefined ? null : value });
            }
          }}>
          {TYPES.map((t) => <option key={t} value={t}>{t}</option>)}
        </select>
        <label htmlFor={id("default")}>Default</label>
        <div>
          {variable.type === "boolean" && !required ? (
            <select id={id("default")} value={text} disabled={readonly}
              onChange={(e) => { setText(e.target.value); commitDefault(e.target.value); }}>
              <option value="true">true</option>
              <option value="false">false</option>
            </select>
          ) : (
            <input id={id("default")} value={required ? "" : text} disabled={readonly || required} autoComplete="off"
              placeholder={required ? "Supplied by each run" : variable.type === "string" ? "(empty text)" : variable.type === "number" ? "0" : variable.type === "array" ? "[]" : "{}"}
              onChange={(e) => setText(e.target.value)} onBlur={() => commitDefault(text)} />
          )}
          <label className="inline-check">
            <input type="checkbox" checked={required} disabled={readonly}
              onChange={(e) => {
                const req = e.target.checked;
                const t = req ? text : text || (variable.type === "boolean" ? "false" : variable.type === "number" ? "0" : variable.type === "array" ? "[]" : variable.type === "object" ? "{}" : "");
                setText(t);
                commitDefault(t, variable.type, req);
              }} />
            Required (no default)
          </label>
        </div>
        <label htmlFor={id("description")}>Description</label>
        <input id={id("description")} value={description} disabled={readonly} placeholder="What this value is for"
          onChange={(e) => setDescription(e.target.value)}
          onBlur={() => description !== (variable.description ?? "") && onChange({ ...variable, description: description || undefined })} />
      </div>
      {(parseError || problems.length > 0) && (
        <div className="field-error" data-testid={`variable-error-${index}`}>
          {parseError && <div>{parseError}</div>}
          {problems.map((p) => <div key={p}>{p}</div>)}
        </div>
      )}
    </li>
  );
}

/** Workflow-level variables: part of the workflow definition (saved with
 * each version). Their values come from the run input, or the default. */
export function VariablesEditor() {
  const variables = useEditorStore((s) => s.definition.variables) ?? [];
  const readonly = useEditorStore((s) => s.mode === "readonly");
  const setVariables = useEditorStore((s) => s.setVariables);
  const issues = useIssues().filter((i) => i.variable !== undefined);

  const update = (i: number, v: WorkflowVariable) => setVariables(variables.map((x, j) => (j === i ? v : x)));
  const add = () => {
    const taken = new Set(variables.map((v) => v.name));
    let n = 1;
    while (taken.has(`variable_${n}`)) n++;
    setVariables([...variables, { name: `variable_${n}`, type: "string", default: "" }]);
  };

  return (
    <div className="variables-editor" data-testid="variables-editor">
      <p className="small muted">
        Reusable values for this workflow. Reference them anywhere as <code>{"{{name}}"}</code>; each run supplies them as input fields of the same name, or uses the default.
      </p>
      {variables.length === 0 && <p className="small muted">No variables yet.</p>}
      <ul className="variable-list">
        {variables.map((v, i) => (
          <VariableRow key={`${i}:${stableStringify(v)}`} index={i} variable={v} readonly={readonly}
            problems={issues.filter((x) => x.variable === v.name).map((x) => x.message)}
            onChange={(next) => update(i, next)}
            onRemove={() => setVariables(variables.filter((_, j) => j !== i))} />
        ))}
      </ul>
      {!readonly && <button type="button" onClick={add}>+ Add variable</button>}
    </div>
  );
}
