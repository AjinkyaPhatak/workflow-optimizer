"use client";

import { useState } from "react";

/** A small read-only JSON tree: collapsible objects/arrays, long strings in
 * scrollable boxes, and a copy button. Not an editor. */
export function JsonViewer({ value, label, defaultDepth = 2 }: { value: unknown; label?: string; defaultDepth?: number }) {
  const [copied, setCopied] = useState(false);
  const text = JSON.stringify(value ?? null, null, 2);
  return (
    <div className="json-viewer" data-testid={label ? `json-${label.toLowerCase()}` : undefined}>
      <div className="json-toolbar">
        {label && <b>{label}</b>}
        <span className="spacer" />
        <button
          className="small-button"
          onClick={() => {
            void navigator.clipboard?.writeText(text).then(
              () => setCopied(true),
              () => setCopied(false),
            );
          }}
        >
          {copied ? "Copied" : "Copy"}
        </button>
      </div>
      <div className="json-body">
        <JsonNode value={value ?? null} depth={0} defaultDepth={defaultDepth} />
      </div>
    </div>
  );
}

function JsonNode({ value, depth, defaultDepth, name }: { value: unknown; depth: number; defaultDepth: number; name?: string }) {
  const [open, setOpen] = useState(depth < defaultDepth);
  const key = name !== undefined ? <span className="json-key">{JSON.stringify(name)}: </span> : null;

  if (value === null || typeof value !== "object") {
    let cls = "json-null";
    let shown = "null";
    if (typeof value === "string") {
      cls = "json-string";
      shown = JSON.stringify(value);
    } else if (typeof value === "number") {
      cls = "json-number";
      shown = String(value);
    } else if (typeof value === "boolean") {
      cls = "json-boolean";
      shown = String(value);
    }
    const long = typeof value === "string" && value.length > 160;
    return (
      <div className="json-line">
        {key}
        {long ? <div className={`json-long ${cls}`}>{shown}</div> : <span className={cls}>{shown}</span>}
      </div>
    );
  }

  const entries = Array.isArray(value) ? value.map((v, i) => [String(i), v] as const) : Object.entries(value as Record<string, unknown>);
  const [openCh, closeCh] = Array.isArray(value) ? ["[", "]"] : ["{", "}"];
  if (entries.length === 0) {
    return (
      <div className="json-line">
        {key}
        {openCh}
        {closeCh}
      </div>
    );
  }
  return (
    <div className="json-line">
      <button className="json-toggle" aria-expanded={open} onClick={() => setOpen(!open)}>
        {open ? "▾" : "▸"}
      </button>
      {key}
      {openCh}
      {!open && (
        <span className="muted">
          {" "}
          {entries.length} {Array.isArray(value) ? (entries.length === 1 ? "item" : "items") : entries.length === 1 ? "key" : "keys"} {closeCh}
        </span>
      )}
      {open && (
        <>
          <div className="json-children">
            {entries.map(([k, v]) => (
              <JsonNode key={k} name={Array.isArray(value) ? undefined : k} value={v} depth={depth + 1} defaultDepth={defaultDepth} />
            ))}
          </div>
          {closeCh}
        </>
      )}
    </div>
  );
}
