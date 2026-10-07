"use client";

import { useLayoutEffect, useRef, useState, type KeyboardEvent } from "react";
import { filterVariables, findOpenReference, insertReference, type VariableGroup, type VariableOption } from "@/lib/workflow/variables";

interface Props {
  id: string;
  value: string;
  onCommit: (v: string) => void;
  /** The references this field can use (lib/workflow/variables). */
  groups: VariableGroup[];
  multiline?: boolean;
  placeholder?: string;
  disabled?: boolean;
  invalid?: boolean;
  describedBy?: string;
}

/** A text field with {{variable}} discovery: typing "{{" (or pressing the
 * insert button) lists the references available to this node, filtered as
 * the user types; choosing one inserts the full expression. Commits on blur
 * (and Enter for single-line fields) as one undo step. It restarts from the
 * stored value whenever that changes (undo, redo, another node). */
export function VariableField(props: Props) {
  return <VariableFieldDraft key={`${props.id}:${props.value}`} {...props} />;
}

type Picker = { start: number; query: string };

function VariableFieldDraft({ id, value, onCommit, groups, multiline, placeholder, disabled, invalid, describedBy }: Props) {
  const [draft, setDraft] = useState(value);
  const [picker, setPicker] = useState<Picker | null>(null);
  const [active, setActive] = useState(0);
  const [caretAfter, setCaretAfter] = useState<number | null>(null);
  const fieldRef = useRef<HTMLInputElement & HTMLTextAreaElement>(null);
  const wrapRef = useRef<HTMLDivElement>(null);

  const shown = picker ? filterVariables(groups, picker.query) : [];
  const flat: VariableOption[] = shown.flatMap((g) => g.options);
  const listId = `${id}-variables`;

  useLayoutEffect(() => {
    if (caretAfter === null || !fieldRef.current) return;
    fieldRef.current.focus();
    fieldRef.current.setSelectionRange(caretAfter, caretAfter);
    setCaretAfter(null);
  }, [caretAfter]);

  const commit = (text = draft) => {
    if (text !== value) onCommit(text);
  };

  const track = (text: string, caret: number) => {
    const open = findOpenReference(text, caret);
    setPicker(open);
    setActive(0);
  };

  const choose = (o: VariableOption) => {
    if (!picker) return;
    const caret = fieldRef.current?.selectionStart ?? draft.length;
    const next = insertReference(draft, picker.start, Math.max(caret, picker.start), o.expression);
    setDraft(next.text);
    setPicker(null);
    setCaretAfter(next.caret);
  };

  const openManually = () => {
    const el = fieldRef.current;
    const caret = el ? el.selectionStart ?? draft.length : draft.length;
    setPicker({ start: caret, query: "" });
    setActive(0);
    el?.focus();
  };

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement | HTMLTextAreaElement>) => {
    if (picker) {
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        if (flat.length) setActive((a) => (a + (e.key === "ArrowDown" ? 1 : flat.length - 1)) % flat.length);
        return;
      }
      if ((e.key === "Enter" || e.key === "Tab") && flat[active]) {
        e.preventDefault();
        choose(flat[active]);
        return;
      }
      if (e.key === "Escape") {
        // Close the list only; the editor must not also clear the selection.
        e.preventDefault();
        e.stopPropagation();
        setPicker(null);
        return;
      }
    }
    if (e.key === "Enter" && !multiline) commit();
  };

  const common = {
    id,
    ref: fieldRef,
    value: draft,
    placeholder,
    disabled,
    "aria-invalid": invalid || undefined,
    "aria-describedby": describedBy,
    role: "combobox",
    "aria-autocomplete": "list" as const,
    "aria-expanded": picker !== null,
    "aria-controls": picker ? listId : undefined,
    "aria-activedescendant": picker && flat[active] ? `${listId}-${active}` : undefined,
    onChange: (e: { target: { value: string; selectionStart: number | null } }) => {
      setDraft(e.target.value);
      track(e.target.value, e.target.selectionStart ?? e.target.value.length);
    },
    onKeyDown,
    onClick: (e: { currentTarget: { value: string; selectionStart: number | null } }) => {
      if (picker) track(e.currentTarget.value, e.currentTarget.selectionStart ?? 0);
    },
    onBlur: (e: { relatedTarget: EventTarget | null }) => {
      if (wrapRef.current?.contains(e.relatedTarget as Node | null)) return;
      setPicker(null);
      commit();
    },
  };

  // Index of each group's first option in the flat (keyboard) order.
  const offsets = shown.map((_, gi) => shown.slice(0, gi).reduce((n, g) => n + g.options.length, 0));
  return (
    <div className="variable-field" ref={wrapRef}>
      <div className="variable-input">
        {multiline ? <textarea rows={4} {...common} /> : <input type="text" autoComplete="off" {...common} />}
        {!disabled && (
          <button
            type="button"
            className="insert-variable"
            title="Insert a variable"
            aria-label="Insert variable"
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => (picker ? setPicker(null) : openManually())}
          >
            {"{ }"}
          </button>
        )}
      </div>
      {picker && (
        <div className="variable-picker" id={listId} role="listbox" aria-label="Available variables" data-testid="variable-picker">
          {shown.map((g, gi) => (
            <div key={g.id} role="group" aria-label={g.label}>
              <div className="picker-group">{g.label}</div>
              {g.options.length === 0 && (
                <div className="picker-empty">
                  {picker.query ? "No match." : g.id === "nodes" ? "Connect nodes before this one to use their outputs." : "Nothing available."}
                </div>
              )}
              {g.options.map((o, oi) => {
                const i = offsets[gi] + oi;
                return (
                  <div
                    key={o.expression}
                    id={`${listId}-${i}`}
                    role="option"
                    aria-selected={i === active}
                    className={`picker-option ${i === active ? "active" : ""}`}
                    onMouseDown={(e) => e.preventDefault()}
                    onMouseEnter={() => setActive(i)}
                    onClick={() => choose(o)}
                    title={o.description}
                  >
                    <code>{`{{${o.expression}}}`}</code>
                    <span className="picker-label">{o.label}</span>
                    {o.type && <span className="picker-type">{o.type}</span>}
                  </div>
                );
              })}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
