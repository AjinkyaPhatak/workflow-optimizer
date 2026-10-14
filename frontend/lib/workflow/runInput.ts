// Run input rows of the editor (the Run tab and test runs share them).

import type { WorkflowVariable } from "@/types/api";

export type RunRow = { key: string; value: string; variable?: WorkflowVariable };

/** A run-input row's value: declared variables are converted to their type
 * (the backend checks it); an empty variable row is left out so its default
 * applies. Other rows are text. */
export function rowValue(r: RunRow): { set: boolean; value?: unknown } {
  const v = r.variable;
  if (!v) return { set: true, value: r.value };
  if (r.value === "") return { set: false };
  switch (v.type) {
    case "string": return { set: true, value: r.value };
    case "number": return { set: true, value: Number.isNaN(Number(r.value)) ? r.value : Number(r.value) };
    case "boolean": return { set: true, value: r.value === "true" ? true : r.value === "false" ? false : r.value };
    default:
      try {
        return { set: true, value: JSON.parse(r.value) };
      } catch {
        return { set: true, value: r.value };
      }
  }
}

export function initialRows(variables: WorkflowVariable[]): RunRow[] {
  return [
    { key: "query", value: "" },
    ...variables.filter((v) => v.name !== "query").map((v) => ({ key: v.name, value: "", variable: v })),
  ];
}

/** The run input object of the rows. */
export function inputOf(rows: RunRow[]): Record<string, unknown> {
  return Object.fromEntries(rows.filter((r) => r.key.trim() && rowValue(r).set).map((r) => [r.key.trim(), rowValue(r).value]));
}
