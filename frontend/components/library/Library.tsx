"use client";

// The Library: uploaded files and knowledge bases on one page. Files are
// what workflows read by reference; knowledge bases group documents the AI
// searches by meaning.

import { useRef, useState } from "react";
import { FILES_ACCEPT, MAX_UPLOAD, formatSize, kindOf } from "@/components/settings/Files";
import { AddDocuments, CreateBase, STATUS, TrySearch } from "@/components/settings/Knowledge";
import { Backdrop, motifOf } from "@/components/ui/Backdrop";
import { ErrorBanner } from "@/components/ui/ErrorBanner";
import { NodeIcon } from "@/components/ui/NodeIcon";
import type { FilesState } from "@/features/files/useFiles";
import type { KnowledgeState } from "@/features/knowledge/useKnowledge";
import { knowledgeApi } from "@/lib/api";
import type { WorkspaceFile } from "@/types/api";

const GLYPHS = {
  doc: "M6 3h8l4 4v14H6zM14 3v4h4M9 13h6M9 17h6",
  image: "M4 5h16v14H4zM4 16l5-5 4 4 3-3 4 4M15.5 9.5h.01",
  audio: "M12 3a3 3 0 013 3v6a3 3 0 01-6 0V6a3 3 0 013-3zM5 11a7 7 0 0014 0M12 18v3",
  table: "M4 5h16v14H4zM4 10h16M10 5v14",
  books: "M5 4h4v16H5zM10 4h4v16h-4zM15 5l3.5-1 3 15.5-3.5 1z",
  upload: "M12 16V4M7 9l5-5 5 5M4 20h16",
  folder: "M3 6h6l2 2h10v11H3z",
};

type Filter = "All" | "Documents" | "Images" | "Recordings";
const FILTERS: Filter[] = ["All", "Documents", "Images", "Recordings"];

const isAudio = (f: WorkspaceFile) => /^audio\/|^video\/|mpeg/.test(f.content_type);
const isImage = (f: WorkspaceFile) => f.content_type.startsWith("image/");

function groupOf(f: WorkspaceFile): Filter {
  return isAudio(f) ? "Recordings" : isImage(f) ? "Images" : "Documents";
}

/** A file's icon and colour by type. */
function lookOf(f: WorkspaceFile): { glyph: string; tone: string; kind: string } {
  if (isAudio(f)) return { glyph: GLYPHS.audio, tone: "tone-charcoal", kind: "Recording" };
  if (isImage(f)) return { glyph: GLYPHS.image, tone: "tone-sun", kind: "Image" };
  const kind = kindOf(f.content_type);
  if (kind === "PDF") return { glyph: GLYPHS.doc, tone: "tone-peach", kind };
  if (kind === "CSV" || kind === "JSON") return { glyph: GLYPHS.table, tone: "tone-teal", kind };
  return { glyph: GLYPHS.doc, tone: kind === "Word" ? "tone-charcoal" : "tone-sandy", kind };
}

function Glyph({ d, size = 18 }: { d: string; size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" aria-hidden fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
      <path d={d} />
    </svg>
  );
}

function FileTile({ f, kn, canEdit, files, onAdded }: {
  f: WorkspaceFile; kn: KnowledgeState; canEdit: boolean; files: FilesState; onAdded: (baseId: string) => void;
}) {
  const [menu, setMenu] = useState<"" | "add">("");
  const [error, setError] = useState<unknown>(null);
  const look = lookOf(f);
  const canIndex = !isAudio(f);
  return (
    <div className={`lib-file${menu ? " menu-open" : ""}`} data-testid={`file-${f.id}`}>
      <span className={`lib-type ${look.tone}`}><Glyph d={look.glyph} /></span>
      <div className="lib-file-text">
        <b title={f.name}>{f.name}</b>
        <span className="muted">{look.kind} · {formatSize(f.size)} · {new Date(f.created_at).toLocaleDateString()}</span>
        <span className="lib-actions">
          <button type="button" className="link-button" onClick={() => void files.download(f)}>Download</button>
          {canEdit && canIndex && kn.bases.length > 0 && (
            <button type="button" className="link-button" aria-expanded={menu === "add"} onClick={() => setMenu(menu ? "" : "add")}>Add to knowledge</button>
          )}
          {canEdit && (
            <button type="button" className="link-button danger" disabled={files.busy === f.id} onClick={() => {
              if (window.confirm(`Delete ${f.name}? Workflows that use it will fail until they use another file.`)) void files.remove(f.id);
            }}>Delete</button>
          )}
        </span>
        <ErrorBanner error={error} />
      </div>
      {menu === "add" && (
        <div className="wf-menu lib-menu" role="menu">
          {kn.bases.map((b) => (
            <button key={b.id} type="button" role="menuitem" onClick={async () => {
              setMenu("");
              setError(null);
              try {
                await knowledgeApi.addFile(b.id, f.id);
                onAdded(b.id);
              } catch (e) {
                setError(e);
              }
            }}>{b.name}</button>
          ))}
        </div>
      )}
    </div>
  );
}

/** One knowledge base up close, in a panel at the side. */
function BasePanel({ kn, files, canEdit, onClose }: { kn: KnowledgeState; files: FilesState; canEdit: boolean; onClose: () => void }) {
  const kb = kn.selected;
  // The panel offers the Library's own (always current) file list.
  const s = { ...kn, files: files.files.filter((f) => !isAudio(f)) };
  return (
    <div className="wf-dialog-backdrop lib-drawer-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <aside className="lib-drawer" role="dialog" aria-modal="true" aria-label={kb?.name ?? "Knowledge base"} data-testid="base-detail">
        {!kb ? <div className="empty"><span className="spinner" aria-hidden /> Loading…</div> : (
          <>
            <div className={`lib-drawer-band motif ${motifOf(kb.id)}`}>
              <span className="wf-icon"><Glyph d={GLYPHS.books} /></span>
              <button type="button" className="wf-dialog-close" aria-label="Close" onClick={onClose}>×</button>
            </div>
            <div className="lib-drawer-body stack">
              <div className="row">
                <h2>{kb.name}</h2>
                <span className="spacer" />
                {canEdit && kb.embedding_model !== "intfloat/multilingual-e5-small" && (
                  <button type="button" disabled={s.busy === "copy"} onClick={() => void s.copyLocal()}>
                    {s.busy === "copy" ? "Copying…" : "Copy to local search"}
                  </button>
                )}
                {canEdit && (
                  <button type="button" disabled={kn.busy === kb.id} onClick={() => {
                    if (window.confirm(`Delete ${kb.name} and its ${kb.items.length} documents? Workflows that search it will fail.`)) {
                      void kn.removeBase(kb.id);
                      onClose();
                    }
                  }}>Delete</button>
                )}
              </div>
              {kb.description && <p className="muted">{kb.description}</p>}
              {kb.items.length === 0 ? <div className="muted small">No documents yet. Add a file or paste some text below.</div> : (
                <ul className="lib-docs">
                  {kb.items.map((d) => (
                    <li key={d.id} data-testid={`doc-${d.id}`}>
                      <span className="lib-file-text">
                        <b>{d.title}</b>
                        <span className="muted small">
                          {d.source === "file" ? "File" : d.source === "url" ? "Web page" : "Text"} · {d.chars.toLocaleString()} characters{d.status === "READY" ? ` · ${d.chunks} passages` : ""}
                        </span>
                        {d.status === "FAILED" && d.error && <span className="field-error small">{d.error}</span>}
                      </span>
                      <span className={`account-status ${STATUS[d.status].tone}`} data-testid="doc-status"><span aria-hidden>●</span> {STATUS[d.status].label}</span>
                      {canEdit && d.status === "FAILED" && <button type="button" disabled={kn.busy === d.id} onClick={() => void kn.reindex(d.id)}>Retry</button>}
                      {canEdit && (
                        <button type="button" className="link-button" disabled={kn.busy === d.id} onClick={() => {
                          if (window.confirm(`Remove ${d.title} from ${kb.name}?`)) void kn.removeDocument(d.id);
                        }}>Remove</button>
                      )}
                    </li>
                  ))}
                </ul>
              )}
              {canEdit && <AddDocuments s={s} />}
              <TrySearch s={s} />
            </div>
          </>
        )}
      </aside>
    </div>
  );
}

export function Library({ files, kn }: { files: FilesState; kn: KnowledgeState }) {
  const canEdit = ["owner", "admin", "member"].includes(files.workspace?.role ?? "");
  const input = useRef<HTMLInputElement>(null);
  const [dragging, setDragging] = useState(false);
  const [tooLarge, setTooLarge] = useState<string[]>([]);
  const [filter, setFilter] = useState<Filter>("All");
  const [creating, setCreating] = useState(false);
  const [panel, setPanel] = useState(false);
  const pick = (list: FileList | null) => {
    const picked = Array.from(list ?? []);
    setTooLarge(picked.filter((f) => f.size > MAX_UPLOAD).map((f) => f.name));
    void files.upload(picked.filter((f) => f.size <= MAX_UPLOAD));
    if (input.current) input.current.value = "";
  };
  const open = (id: string) => {
    kn.select(id);
    setPanel(true);
  };
  const shown = filter === "All" ? files.files : files.files.filter((f) => groupOf(f) === filter);
  const loading = files.loading || kn.loading;

  return (
    <main className={`wf-page textured lib${dragging ? " dragging" : ""}`} data-testid="files"
      onDragOver={(e) => {
        if (!canEdit) return;
        e.preventDefault();
        setDragging(true);
      }}
      onDragLeave={(e) => e.currentTarget === e.target && setDragging(false)}
      onDrop={(e) => {
        if (!canEdit) return;
        e.preventDefault();
        setDragging(false);
        pick(e.dataTransfer.files);
      }}>
      <section className="wf-hero lib-hero">
        <Backdrop icons={[
          <Glyph key="0" d={GLYPHS.doc} />, <NodeIcon key="1" def={{ type: "transcribe_audio", category: "" }} size={16} />,
          <Glyph key="2" d={GLYPHS.image} />, <Glyph key="3" d={GLYPHS.books} />,
        ]} />
        <div className="wf-hero-inner">
          <span className="wf-eyebrow">Library</span>
          <h1>Everything your workflows can read</h1>
          <p>Upload files once and use them in any workflow, or group them into knowledge the AI can search.</p>
          {canEdit && (
            <div className="lib-upload" data-testid="dropzone">
              <span className="lib-upload-icon"><Glyph d={GLYPHS.upload} size={16} /></span>
              <span>Drop files anywhere on this page, or</span>
              <button type="button" className="link-button" disabled={files.busy === "upload"} onClick={() => input.current?.click()}>
                {files.busy === "upload" ? "uploading…" : "choose files"}
              </button>
              <input ref={input} type="file" multiple hidden accept={FILES_ACCEPT} aria-label="Upload files" onChange={(e) => pick(e.target.files)} />
            </div>
          )}
          <span className="lib-formats">PDF, Word, text, CSV, images and recordings · up to 20 MB each</span>
          {files.workspaces.length > 1 && (
            <select aria-label="Workspace" className="wf-select tpl-workspace" value={files.workspace?.id ?? ""} onChange={(e) => {
              files.setWorkspaceId(e.target.value);
              kn.setWorkspaceId(e.target.value);
            }}>
              {files.workspaces.map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}
            </select>
          )}
        </div>
      </section>

      <section className="wf-list">
        <ErrorBanner error={files.error ?? kn.error} />
        {tooLarge.length > 0 && <div className="notice error" role="alert">Larger than 20 MB, not uploaded: {tooLarge.join(", ")}</div>}

        <div className="tpl-group-head">
          <span className="tpl-group-icon tone-teal"><Glyph d={GLYPHS.books} size={14} /></span>
          <h2>Knowledge bases</h2>
          <span className="muted">Documents the AI can search by meaning</span>
        </div>
        {loading ? (
          <div className="wf-grid">{[0, 1, 2].map((i) => <div key={i} className="wf-card wf-skeleton" aria-hidden />)}</div>
        ) : (
          <div className="wf-grid" data-testid="knowledge">
            {kn.bases.map((b) => (
              <button key={b.id} type="button" className="wf-card lib-base" onClick={() => open(b.id)} data-testid={`base-${b.id}`}>
                <span className={`wf-band motif ${motifOf(b.id)}`}><span className="wf-icon"><Glyph d={GLYPHS.books} /></span></span>
                <span className="wf-body">
                  <b className="wf-name">{b.name}</b>
                  <span className="wf-status"><span className="dot" aria-hidden />{b.documents} {b.documents === 1 ? "document" : "documents"}</span>
                </span>
              </button>
            ))}
            {canEdit && (
              <button type="button" className="wf-card wf-empty-tile" onClick={() => setCreating(true)}>
                <span className="wf-plus" aria-hidden>+</span>
                {kn.bases.length === 0 ? "Create your first knowledge base" : "New knowledge base"}
              </button>
            )}
            {!canEdit && kn.bases.length === 0 && <p className="muted" data-testid="no-bases">No knowledge bases yet.</p>}
          </div>
        )}

        <div className="tpl-group-head lib-files-head">
          <span className="tpl-group-icon tone-peach"><Glyph d={GLYPHS.folder} size={14} /></span>
          <h2>Files</h2>
          {!loading && <span className="muted">{files.files.length}</span>}
          <span className="spacer" />
          <div className="tpl-chips lib-filters" role="tablist" aria-label="File types">
            {FILTERS.map((f) => (
              <button key={f} type="button" role="tab" aria-selected={f === filter} className={f === filter ? "active" : ""} onClick={() => setFilter(f)}>{f}</button>
            ))}
          </div>
        </div>
        {loading ? null : files.files.length === 0 ? (
          <div className="lib-empty" data-testid="no-files">
            <b>No files yet</b>
            <span className="muted">Drop a PDF, a spreadsheet or a recording here to use it in your workflows.</span>
          </div>
        ) : shown.length === 0 ? (
          <p className="muted">No {filter.toLowerCase()} yet.</p>
        ) : (
          <div className="lib-files">
            {shown.map((f) => (
              <FileTile key={f.id} f={f} kn={kn} canEdit={canEdit} files={files} onAdded={open} />
            ))}
          </div>
        )}
      </section>

      {dragging && <div className="lib-drop-overlay" aria-hidden><span><Glyph d={GLYPHS.upload} size={22} /> Drop to upload</span></div>}

      {creating && (
        <div className="wf-dialog-backdrop" onMouseDown={(e) => e.target === e.currentTarget && setCreating(false)}>
          <div className="wf-dialog" role="dialog" aria-modal="true" aria-label="New knowledge base">
            <button type="button" className="wf-dialog-close" aria-label="Close" onClick={() => setCreating(false)}>×</button>
            <CreateBase s={{
              ...kn,
              create: async (name, description, credentialId) => {
                const ok = await kn.create(name, description, credentialId);
                if (ok) setCreating(false);
                return ok;
              },
            }} />
          </div>
        </div>
      )}
      {panel && <BasePanel kn={kn} files={files} canEdit={canEdit} onClose={() => {
        setPanel(false);
        kn.select(null);
      }} />}
    </main>
  );
}
