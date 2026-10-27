"use client";
import { useEffect, useMemo, useRef, useState, type DragEvent, type KeyboardEvent } from "react";
import { createPortal } from "react-dom";
import { NodeIcon } from "@/components/ui/NodeIcon";
import type { ToolDescriptor } from "@/types/automation";
import type { ControlDescriptor } from "@/lib/automation/editor";
import { toolIcon } from "./PlanNode";
export const PLAN_DRAG_TYPE = "application/workflow-plan-step";
export type PaletteItem = { id: string; name: string; description?: string; tool?: ToolDescriptor; control?: ControlDescriptor };
const tabs = [{ id: "basics", label: "Basics" }, { id: "ai", label: "AI" }, { id: "knowledge", label: "Knowledge" }, { id: "apps", label: "Apps" }, { id: "data", label: "Data & web" }, { id: "media", label: "Media" }, { id: "logic", label: "Logic" }];
type Tile = PaletteItem & { actions?: PaletteItem[]; integration?: string };
export function PlanPalette({ tools, controls, onAdd, disabled }: { tools: ToolDescriptor[]; controls: ControlDescriptor[]; onAdd: (item: PaletteItem) => void; disabled?: boolean }) {
  const [tab, setTab] = useState("basics"), [search, setSearch] = useState(""), [active, setActive] = useState(0);
  const [menu, setMenu] = useState<{ tile: Tile; left: number; top: number } | null>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const items = useMemo(() => [
    ...tools.map(tool => {
      const icon = toolIcon(tool);
      const declared = (tool as ToolDescriptor & { category?: string }).category;
      const category = declared && tabs.some(tab => tab.id === declared) ? declared : icon.integration || tool.provider ? "apps" : icon.category === "ai" || icon.category === "llm" ? "ai" : icon.category === "knowledge_base" ? "knowledge" : /^(web|table|analysis)\./.test(tool.id) ? "data" : "basics";
      return { id: tool.id, name: tool.name, description: tool.description, tool, category };
    }),
    ...controls.map(control => ({ id: "control:" + control.kind, name: control.name, description: control.description, control, category: "logic" })),
  ], [tools, controls]);
  const shown: Tile[] = useMemo(() => {
    const visible = items.filter(item => search.trim() ? (item.name + " " + item.description).toLowerCase().includes(search.trim().toLowerCase()) : item.category === tab);
    if (tab !== "apps" || search.trim()) return visible;
    const groups = new Map<string, Tile>(); const others: Tile[] = [];
    for (const item of visible) {
      const integration = "tool" in item ? toolIcon(item.tool).integration?.id : undefined;
      if (!integration) { others.push(item); continue; }
      const previous = groups.get(integration);
      if (previous) previous.actions!.push(item);
      else groups.set(integration, { id: "app:" + integration, name: integration === "gmail" ? "Gmail" : integration === "whatsapp" ? "WhatsApp" : integration.split("_").map(word => word[0].toUpperCase() + word.slice(1)).join(" "), actions: [item], integration });
    }
    return [...groups.values()].sort((a, b) => a.name.localeCompare(b.name)).concat(others);
  }, [items, search, tab]);
  useEffect(() => {
    if (!menu) return;
    const close = (event: MouseEvent) => { if (!menuRef.current?.contains(event.target as Node)) setMenu(null); };
    const escape = (event: globalThis.KeyboardEvent) => { if (event.key === "Escape") setMenu(null); };
    document.addEventListener("mousedown", close); window.addEventListener("keydown", escape);
    return () => { document.removeEventListener("mousedown", close); window.removeEventListener("keydown", escape); };
  }, [menu]);
  function keyboard(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key === "ArrowRight" || event.key === "ArrowDown") { event.preventDefault(); setActive(value => Math.min(shown.length - 1, value + 1)); }
    if (event.key === "ArrowLeft" || event.key === "ArrowUp") { event.preventDefault(); setActive(value => Math.max(0, value - 1)); }
    if (event.key === "Enter" && shown[active] && !disabled && !shown[active].actions) { event.preventDefault(); onAdd(shown[active]); }
    if (event.key === "Escape") { setSearch(""); event.currentTarget.blur(); }
  }
  const drag = (event: DragEvent, item: PaletteItem) => { event.dataTransfer.setData(PLAN_DRAG_TYPE, item.id); event.dataTransfer.effectAllowed = "copy"; };
  return <div className="palette-bar plan-palette" aria-label="Add workflow steps">
    <div className="palette-bar-top"><div className="palette-tabs" role="tablist" aria-label="Step categories">{tabs.map(item => <button key={item.id} type="button" role="tab" aria-selected={tab === item.id && !search} className={"palette-tab " + (tab === item.id && !search ? "selected" : "")} onClick={() => { setTab(item.id); setSearch(""); setActive(0); setMenu(null); }}>{item.label}</button>)}</div>
      <span className="spacer" /><input className="palette-search" aria-label="Search steps" placeholder="Search steps…" value={search} onChange={e => { setSearch(e.target.value); setActive(0); setMenu(null); }} onKeyDown={keyboard} />
    </div>
    <div className="palette-row">{shown.map((item, index) => <button type="button" key={item.id} className={"palette-tile " + (search && index === active ? "active" : "")} title={item.description} disabled={disabled} draggable={!disabled && !item.actions} aria-haspopup={item.actions ? "menu" : undefined} onDragStart={event => drag(event, item)} onClick={event => {
      if (item.actions) { const box = event.currentTarget.getBoundingClientRect(); setMenu({ tile: item, left: Math.max(8, Math.min(box.left, window.innerWidth - 308)), top: box.bottom + 6 }); }
      else onAdd(item);
    }}>
      <span className="tile-icon"><NodeIcon integrationId={item.integration} def={toolIcon(item.tool, item.control?.kind ?? "tool")} size={22} /></span><span className="tile-label">{item.name}{item.actions ? " ▾" : ""}</span>
    </button>)}{!shown.length && <span className="palette-count">{tools.length ? "No matching steps" : "Loading available steps…"}</span>}</div>
    {menu && !disabled && createPortal(<div className="palette-menu" ref={menuRef} role="menu" aria-label={menu.tile.name + " actions"} style={{ left: menu.left, top: menu.top }}><div className="palette-menu-title">{menu.tile.name}</div>{menu.tile.actions?.map(item => <button key={item.id} className="palette-menu-item" role="menuitem" onClick={() => { onAdd(item); setMenu(null); }} draggable onDragStart={event => { drag(event, item); setMenu(null); }}><span className="palette-menu-name">{item.name}</span><span className="palette-menu-desc">{item.description}</span></button>)}</div>, document.body)}
  </div>;
}
