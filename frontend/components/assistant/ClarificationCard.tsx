"use client";

import { useId, useState, type FormEvent } from "react";
import { ErrorBanner } from "@/components/ui/ErrorBanner";
import { AiMarkdown } from "@/components/ui/AiMarkdown";
import type { Question } from "@/types/automation";

export function ClarificationCard({ question: q, onAnswer, busy = false }: {
  question: Question; onAnswer: (value: unknown) => Promise<void>; busy?: boolean;
}) {
  const id = useId();
  const [value, setValue] = useState("");
  const [other, setOther] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [sending, setSending] = useState(false);
  const choosing = ["choice", "contact", "file", "boolean"].includes(q.kind);
  async function submit(e: FormEvent) {
    e.preventDefault(); setError(null); setSending(true);
    const text = value === "__other" ? other : value;
    const answer = q.kind === "number" ? (text.trim() === "" ? null : Number(text)) : q.kind === "boolean" ? text === "true" : text;
    try { await onAnswer(answer); } catch (err) { setError(err); } finally { setSending(false); }
  }
  return <form className="clarification-card" onSubmit={submit} aria-busy={sending}>
    <div className="clarification-eyebrow">{q.purpose === "intent" ? "Help me shape the result" : "A little detail before I continue"}</div>
    {(q.step_name || q.field_label) && <p className="hint" data-testid="clarification-location"><strong>{q.step_name}</strong>{q.step_name && q.field_label ? " · " : ""}{q.field_label}</p>}
    {q.context && <div className="clarification-context"><AiMarkdown text={q.context} /></div>}
    <label htmlFor={id}><AiMarkdown text={q.prompt} /></label>
    {choosing ? <select id={id} value={value} onChange={e => setValue(e.target.value)} required={q.required} disabled={busy || sending}>
      <option value="">Choose an option…</option>
      {q.kind === "boolean" ? <><option value="true">Yes</option><option value="false">No</option></> :
        (q.options ?? []).map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
      {q.allow_other && <option value="__other">Other — I’ll type it</option>}
    </select> : q.purpose === "intent" ? <textarea id={id} rows={3} value={value} onChange={e => setValue(e.target.value)} required={q.required} placeholder={q.example || "Describe the result or rule you want."} disabled={busy || sending} /> : <input id={id} type={q.kind === "number" ? "number" : q.kind === "date" ? "date" : "text"}
      value={value} onChange={e => setValue(e.target.value)} required={q.required} min={q.min ?? undefined} max={q.max ?? undefined}
      step="any" placeholder={q.example || undefined} disabled={busy || sending} />}
    {value === "__other" && <input aria-label="Your other answer" value={other} onChange={e => setOther(e.target.value)} required disabled={busy || sending} />}
    {q.example && <p className="hint">Example: {q.example}</p>}
    {q.purpose === "intent" && <p className="hint">I’ll use your answer to update the workflow.</p>}
    <ErrorBanner error={error} />
    <button className="primary" disabled={busy || sending} type="submit">{sending ? "Checking…" : q.purpose === "intent" ? "Update workflow" : "Continue"}</button>
  </form>;
}
