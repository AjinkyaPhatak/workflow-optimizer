"use client";

import { useRef, useState } from "react";
import { ErrorBanner } from "@/components/ui/ErrorBanner";
import type { FilesState } from "@/features/files/useFiles";

/** 1536 -> "1.5 KB". */
export function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1).replace(/\.0$/, "")} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1).replace(/\.0$/, "")} MB`;
}

const KINDS: [RegExp, string][] = [
  [/pdf/, "PDF"], [/wordprocessingml/, "Word"], [/^image\//, "Image"], [/csv/, "CSV"], [/markdown/, "Markdown"],
  [/html/, "HTML"], [/json/, "JSON"], [/^text\//, "Text"],
];

export function kindOf(contentType: string): string {
  return KINDS.find(([re]) => re.test(contentType))?.[1] ?? "File";
}

export const MAX_UPLOAD = 20 * 1024 * 1024;
/** Documents (text can be read from them). */
export const ACCEPT = ".pdf,.docx,.txt,.md,.csv,.tsv,.json,.html,.htm,.xml,.png,.jpg,.jpeg,.gif,.webp";
/** Everything the Files page takes: documents, images and recordings (Phase G). */
export const FILES_ACCEPT = `${ACCEPT},.mp3,.m4a,.wav,.ogg,.oga,.opus,.webm,.flac,.mp4,.mpeg,.mpga`;

/** Files: documents and images workflows read by reference (Extract Text,
 * knowledge bases, images for the LLM node). */
export function Files({ state, confirm = (m) => window.confirm(m) }: { state: FilesState; confirm?: (message: string) => boolean }) {
  const s = state;
  const canEdit = s.workspace?.role === "owner" || s.workspace?.role === "admin" || s.workspace?.role === "member";
  const input = useRef<HTMLInputElement>(null);
  const [dragging, setDragging] = useState(false);
  const [tooLarge, setTooLarge] = useState<string[]>([]);

  const pick = (list: FileList | null) => {
    const picked = Array.from(list ?? []);
    setTooLarge(picked.filter((f) => f.size > MAX_UPLOAD).map((f) => f.name));
    void s.upload(picked.filter((f) => f.size <= MAX_UPLOAD));
    if (input.current) input.current.value = "";
  };

  return (
    <main className="dashboard stack" data-testid="files">
      <div className="row">
        <h1>Files</h1>
        <span className="spacer" />
        {s.workspaces.length > 1 && (
          <select aria-label="Workspace" style={{ width: 240 }} value={s.workspace?.id ?? ""} onChange={(e) => s.setWorkspaceId(e.target.value)}>
            {s.workspaces.map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}
          </select>
        )}
      </div>
      <p className="muted">
        Documents, images and recordings your workflows can use: read a document, search your knowledge, show a picture to the AI, or turn a recording into text. Up to 20 MB each.
      </p>
      <ErrorBanner error={s.error} />
      {tooLarge.length > 0 && <div className="notice error" role="alert">Larger than 20 MB, not uploaded: {tooLarge.join(", ")}</div>}
      {canEdit && (
        <section className={`card dropzone ${dragging ? "dragging" : ""}`} data-testid="dropzone"
          onDragOver={(e) => {
            e.preventDefault();
            setDragging(true);
          }}
          onDragLeave={() => setDragging(false)}
          onDrop={(e) => {
            e.preventDefault();
            setDragging(false);
            pick(e.dataTransfer.files);
          }}>
          <div>Drop files here, or</div>
          <button type="button" className="primary" disabled={s.busy === "upload"} onClick={() => input.current?.click()}>
            {s.busy === "upload" ? "Uploading…" : "Choose files"}
          </button>
          <input ref={input} type="file" multiple hidden accept={FILES_ACCEPT} aria-label="Upload files" onChange={(e) => pick(e.target.files)} />
          <div className="muted small">PDF, Word, text, CSV, images (PNG, JPEG, GIF, WebP) and recordings (MP3, M4A, WAV, OGG, WebM, FLAC, MP4)</div>
        </section>
      )}
      {s.loading ? (
        <div className="empty"><span className="spinner" aria-hidden /> Loading…</div>
      ) : s.files.length === 0 ? (
        <section className="card empty" data-testid="no-files">No files yet.</section>
      ) : (
        <section className="card">
          <ul className="account-list">
            {s.files.map((f) => (
              <li key={f.id} className="account-row" data-testid={`file-${f.id}`}>
                <div className="account-who">
                  <b>{f.name}</b>
                  <span className="muted small">{kindOf(f.content_type)} · {formatSize(f.size)} · {new Date(f.created_at).toLocaleDateString()}</span>
                </div>
                <span className="spacer" />
                <button type="button" onClick={() => void s.download(f)}>Download</button>
                {canEdit && (
                  <button type="button" disabled={s.busy === f.id}
                    onClick={() => {
                      if (confirm(`Delete ${f.name}? Workflows that use it will fail until they use another file.`)) void s.remove(f.id);
                    }}>
                    {s.busy === f.id ? "Deleting…" : "Delete"}
                  </button>
                )}
              </li>
            ))}
          </ul>
        </section>
      )}
    </main>
  );
}
