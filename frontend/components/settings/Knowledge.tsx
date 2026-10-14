"use client";

import Link from "next/link";
import { useRef, useState } from "react";
import { ACCEPT, MAX_UPLOAD } from "@/components/settings/Files";
import { ErrorBanner } from "@/components/ui/ErrorBanner";
import type { KnowledgeState } from "@/features/knowledge/useKnowledge";
import type { KnowledgeDocumentStatus, KnowledgeResult } from "@/types/api";

const STATUS: Record<KnowledgeDocumentStatus, { label: string; tone: string }> = {
  INDEXING: { label: "Indexing…", tone: "off" },
  READY: { label: "Ready", tone: "ok" },
  FAILED: { label: "Failed", tone: "bad" },
};

function CreateBase({ s }: { s: KnowledgeState }) {
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [credentialId, setCredentialId] = useState("");
  const credential = credentialId || s.credentials[0]?.id || "";
  return (
    <section className="card stack" data-testid="create-base">
      <h2>New knowledge base</h2>
      <div className="field">
        <label htmlFor="kb-name">Name</label>
        <input id="kb-name" value={name} placeholder="e.g. Support policies" onChange={(e) => setName(e.target.value)} />
      </div>
      <div className="field">
        <label htmlFor="kb-desc">Description</label>
        <input id="kb-desc" value={description} onChange={(e) => setDescription(e.target.value)} />
      </div>
      <div className="field">
        <label htmlFor="kb-cred">OpenAI credential</label>
        {s.credentials.length === 0 ? (
          <div className="hint">Add an OpenAI API key on the <Link href="/settings/credentials">Credentials</Link> page first.</div>
        ) : (
          <select id="kb-cred" value={credential} onChange={(e) => setCredentialId(e.target.value)}>
            {s.credentials.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
          </select>
        )}
        <div className="field-help">Used to create the embeddings (OpenAI text-embedding-3-small).</div>
      </div>
      <div className="row">
        <span className="spacer" />
        <button type="button" className="primary" disabled={!name.trim() || !credential || s.busy === "create"}
          onClick={async () => {
            if (await s.create(name.trim(), description.trim(), credential)) {
              setName("");
              setDescription("");
            }
          }}>
          {s.busy === "create" ? "Creating…" : "Create"}
        </button>
      </div>
    </section>
  );
}

function AddDocuments({ s }: { s: KnowledgeState }) {
  const [title, setTitle] = useState("");
  const [text, setText] = useState("");
  const [fileId, setFileId] = useState("");
  const input = useRef<HTMLInputElement>(null);
  const busy = s.busy === "add";
  return (
    <section className="card stack" data-testid="add-documents">
      <h3>Add documents</h3>
      <div className="row" style={{ gap: 8, flexWrap: "wrap" }}>
        <select aria-label="File to add" value={fileId} onChange={(e) => setFileId(e.target.value)} style={{ flex: 1, minWidth: 200 }}>
          <option value="">Choose an uploaded file…</option>
          {s.files.map((f) => <option key={f.id} value={f.id}>{f.name}</option>)}
        </select>
        <button type="button" disabled={!fileId || busy} onClick={async () => {
          await s.addFiles([fileId]);
          setFileId("");
        }}>Add file</button>
        <button type="button" disabled={busy} onClick={() => input.current?.click()}>Upload and add…</button>
        <input ref={input} type="file" multiple hidden accept={ACCEPT} aria-label="Upload documents"
          onChange={(e) => {
            const picked = Array.from(e.target.files ?? []).filter((f) => f.size <= MAX_UPLOAD);
            void s.uploadAndAdd(picked);
            e.target.value = "";
          }} />
      </div>
      <div className="field">
        <label htmlFor="doc-title">Or paste text: title</label>
        <input id="doc-title" value={title} placeholder="Optional" onChange={(e) => setTitle(e.target.value)} />
      </div>
      <div className="field">
        <label htmlFor="doc-text">Text</label>
        <textarea id="doc-text" rows={4} value={text} onChange={(e) => setText(e.target.value)} />
      </div>
      <div className="row">
        <span className="spacer" />
        <button type="button" className="primary" disabled={!text.trim() || busy} onClick={async () => {
          if (await s.addText(title.trim(), text)) {
            setTitle("");
            setText("");
          }
        }}>{busy ? "Adding…" : "Add text"}</button>
      </div>
    </section>
  );
}

function TrySearch({ s }: { s: KnowledgeState }) {
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<KnowledgeResult[] | null>(null);
  return (
    <section className="card stack" data-testid="try-search">
      <h3>Try a search</h3>
      <div className="row" style={{ gap: 8 }}>
        <input aria-label="Search question" value={query} placeholder="Ask a question…" style={{ flex: 1 }} onChange={(e) => setQuery(e.target.value)}
          onKeyDown={async (e) => {
            if (e.key === "Enter" && query.trim()) setResults(await s.search(query.trim()));
          }} />
        <button type="button" disabled={!query.trim() || s.busy === "search"} onClick={async () => setResults(await s.search(query.trim()))}>
          {s.busy === "search" ? "Searching…" : "Search"}
        </button>
      </div>
      {results && (results.length === 0 ? <div className="muted small">No passages found.</div> : (
        <ol className="kb-results">
          {results.map((r, i) => (
            <li key={i}>
              <div className="small muted">{r.document_title} · similarity {r.score.toFixed(2)}</div>
              <div className="kb-passage">{r.text}</div>
            </li>
          ))}
        </ol>
      ))}
    </section>
  );
}

/** Knowledge: bases of documents that workflows search by meaning
 * (Knowledge Search node) and add to (Add to Knowledge Base node). */
export function Knowledge({ state, confirm = (m) => window.confirm(m) }: { state: KnowledgeState; confirm?: (message: string) => boolean }) {
  const s = state;
  const canEdit = s.workspace?.role === "owner" || s.workspace?.role === "admin" || s.workspace?.role === "member";
  const kb = s.selected;
  return (
    <main className="dashboard stack" data-testid="knowledge">
      <div className="row">
        <h1>Knowledge</h1>
        <span className="spacer" />
        {s.workspaces.length > 1 && (
          <select aria-label="Workspace" style={{ width: 240 }} value={s.workspace?.id ?? ""} onChange={(e) => s.setWorkspaceId(e.target.value)}>
            {s.workspaces.map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}
          </select>
        )}
      </div>
      <p className="muted">
        Knowledge bases hold documents that workflows search by meaning: use Knowledge Search to give an LLM the right passages.
      </p>
      <ErrorBanner error={s.error} />
      {s.loading ? <div className="empty"><span className="spinner" aria-hidden /> Loading…</div> : (
        <div className="kb-layout">
          <aside className="stack">
            <section className="card">
              {s.bases.length === 0 ? <div className="muted small" data-testid="no-bases">No knowledge bases yet.</div> : (
                <ul className="kb-list">
                  {s.bases.map((b) => (
                    <li key={b.id}>
                      <button type="button" className={`kb-item ${kb?.id === b.id ? "active" : ""}`} onClick={() => s.select(b.id)} data-testid={`base-${b.id}`}>
                        <b>{b.name}</b>
                        <span className="muted small">{b.documents} {b.documents === 1 ? "document" : "documents"}</span>
                      </button>
                    </li>
                  ))}
                </ul>
              )}
            </section>
            {canEdit && <CreateBase s={s} />}
          </aside>
          <section className="stack">
            {!kb ? <div className="card empty">Choose a knowledge base, or create one.</div> : (
              <>
                <section className="card stack" data-testid="base-detail">
                  <div className="row">
                    <h2>{kb.name}</h2>
                    <span className="spacer" />
                    {canEdit && (
                      <button type="button" disabled={s.busy === kb.id} onClick={() => {
                        if (confirm(`Delete ${kb.name} and its ${kb.items.length} documents? Workflows that search it will fail.`)) void s.removeBase(kb.id);
                      }}>Delete</button>
                    )}
                  </div>
                  {kb.description && <p className="muted">{kb.description}</p>}
                  {kb.items.length === 0 ? <div className="muted small">No documents yet.</div> : (
                    <ul className="account-list">
                      {kb.items.map((d) => (
                        <li key={d.id} className="account-row" data-testid={`doc-${d.id}`}>
                          <div className="account-who">
                            <b>{d.title}</b>
                            <span className="muted small">
                              {d.source === "file" ? "File" : "Text"} · {d.chars.toLocaleString()} characters
                              {d.status === "READY" ? ` · ${d.chunks} passages` : ""}
                            </span>
                            {d.status === "FAILED" && d.error && <span className="field-error small">{d.error}</span>}
                          </div>
                          <span className={`account-status ${STATUS[d.status].tone}`} data-testid="doc-status">
                            <span aria-hidden>●</span> {STATUS[d.status].label}
                          </span>
                          <span className="spacer" />
                          {canEdit && d.status === "FAILED" && (
                            <button type="button" disabled={s.busy === d.id} onClick={() => void s.reindex(d.id)}>Retry</button>
                          )}
                          {canEdit && (
                            <button type="button" disabled={s.busy === d.id} onClick={() => {
                              if (confirm(`Remove ${d.title} from ${kb.name}?`)) void s.removeDocument(d.id);
                            }}>Remove</button>
                          )}
                        </li>
                      ))}
                    </ul>
                  )}
                </section>
                {canEdit && <AddDocuments s={s} />}
                <TrySearch s={s} />
              </>
            )}
          </section>
        </div>
      )}
    </main>
  );
}
