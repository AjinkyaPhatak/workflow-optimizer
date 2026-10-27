"use client";

// A run's result shown the way people read it (Phase I1): text as text, a
// list of records as a table, a record as labelled lines; JSON only behind
// "Show technical details".

import { useState } from "react";
import { questionLabel } from "@/lib/workflow/runFields";
import { useShowDetails } from "@/stores/ui/details";
import { AiMarkdown } from "@/components/ui/AiMarkdown";

const isRecord = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const isScalar = (v: unknown) => v === null || ["string", "number", "boolean"].includes(typeof v);

function scalarText(v: unknown): string {
  if (v === null || v === undefined) return "—";
  if (typeof v === "boolean") return v ? "Yes" : "No";
  return String(v);
}

function Text({ value }: { value: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="friendly-text">
      <AiMarkdown text={value} className="friendly-text-body" />
      <button type="button" className="small-button" onClick={() => {
        void navigator.clipboard?.writeText(value).then(() => setCopied(true), () => {});
      }}>{copied ? "Copied" : "Copy"}</button>
    </div>
  );
}

function Table({ rows }: { rows: Record<string, unknown>[] }) {
  const columns = [...new Set(rows.flatMap((r) => Object.keys(r)))].slice(0, 8);
  return (
    <div className="friendly-table-wrap">
      <table className="friendly-table">
        <thead><tr>{columns.map((c) => <th key={c}>{questionLabel(c)}</th>)}</tr></thead>
        <tbody>
          {rows.slice(0, 50).map((r, i) => (
            <tr key={i}>{columns.map((c) => <td key={c}>{typeof r[c] === "string" ? <AiMarkdown text={r[c] as string} /> : isScalar(r[c]) ? scalarText(r[c]) : JSON.stringify(r[c])}</td>)}</tr>
          ))}
        </tbody>
      </table>
      {rows.length > 50 && <div className="small muted">…and {rows.length - 50} more</div>}
    </div>
  );
}

/** Renders any result value readably. */
export function FriendlyValue({ value, depth = 0 }: { value: unknown; depth?: number }) {
  const details = useShowDetails();
  // Long or top-level text gets a box with Copy; a short value in a record
  // is shown inline.
  if (typeof value === "string") return depth === 0 || value.length > 120 || value.includes("\n") ? <Text value={value} /> : <AiMarkdown text={value} className="friendly-scalar" />;
  if (isScalar(value)) return <div className="friendly-scalar">{scalarText(value)}</div>;
  if (Array.isArray(value)) {
    if (value.length === 0) return <div className="muted">Nothing.</div>;
    if (value.every(isRecord)) return <Table rows={value as Record<string, unknown>[]} />;
    if (value.every(isScalar)) return <ul className="friendly-list">{value.map((v, i) => <li key={i}>{typeof v === "string" ? <AiMarkdown text={v} /> : scalarText(v)}</li>)}</ul>;
  }
  if (isRecord(value) && depth < 2) {
    const entries = Object.entries(value);
    if (entries.length === 0) return <div className="muted">Nothing.</div>;
    return (
      <dl className="friendly-record">
        {entries.map(([k, v]) => (
          <div key={k}>
            <dt>{questionLabel(k)}</dt>
            <dd><FriendlyValue value={v} depth={depth + 1} /></dd>
          </div>
        ))}
      </dl>
    );
  }
  return details || depth > 0 ? <pre className="json small">{JSON.stringify(value, null, 2)}</pre> : <div className="muted">A result that is hard to show here. Turn on technical details to see it.</div>;
}
