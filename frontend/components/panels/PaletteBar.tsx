"use client";

import { useProviders } from "@/features/connected-accounts/useProviders";
import { unavailableApps } from "@/lib/workflow/availability";
import { useEffect, useMemo, useRef, useState, type DragEvent, type KeyboardEvent } from "react";
import { NodeInfoCard } from "@/components/nodes/NodeInfoCard";
import { NodeIcon } from "@/components/ui/NodeIcon";
import { Popover, type AnchorRect } from "@/components/ui/Popover";
import { NODE_DRAG_TYPE, actionName } from "@/features/nodes/catalog";
import { PALETTE_TABS, searchTiles, tilesFor, type PaletteTab, type Tile } from "@/features/nodes/palette";
import { useEditorContext } from "@/features/workflows/EditorContext";
import { useEditorStore } from "@/stores/workflow-editor/store";
import type { NodeDefinition } from "@/types/api";

const HOVER_DELAY_MS = 450;

function rectOf(el: Element): AnchorRect {
  const r = el.getBoundingClientRect();
  return { left: r.left, right: r.right, top: r.top, bottom: r.bottom };
}

/** The palette bar above the canvas (Phase H1): tabs of tiles, generated
 * from GET /api/v1/nodes. Click a tile (or press Enter) to add it, or drag
 * it onto the canvas. An app tile opens a menu of what the app can do.
 * Searching lists every matching node; ↑ ↓ choose, Enter adds, Esc clears. */
export function PaletteBar({ onAdd }: { onAdd: (def: NodeDefinition) => void }) {
  const { catalogList } = useEditorContext();
  const readonly = useEditorStore((s) => s.mode === "readonly");
  const [tab, setTab] = useState<PaletteTab>("basics");
  const [filter, setFilter] = useState("");
  const [active, setActive] = useState(0);
  const [menu, setMenu] = useState<{ tile: Extract<Tile, { kind: "app" }>; anchor: AnchorRect } | null>(null);
  const [hover, setHover] = useState<{ def: NodeDefinition; anchor: AnchorRect } | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const rowRef = useRef<HTMLDivElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);

  const searching = filter.trim() !== "";
  // Apps whose sign-in is not set up on the server are Coming soon.
  const providers = useProviders();
  const unavailable = useMemo(() => unavailableApps(catalogList, providers), [catalogList, providers]);
  const tiles = useMemo(() => (searching ? searchTiles(catalogList, filter, unavailable) : tilesFor(catalogList, tab, unavailable)),
    [catalogList, tab, filter, searching, unavailable]);
  const counts = useMemo(() => new Map(PALETTE_TABS.map((t) => [t.id, tilesFor(catalogList, t.id).length])), [catalogList]);
  const activeIndex = Math.min(active, Math.max(tiles.length - 1, 0));

  const hideHover = () => {
    if (timer.current) clearTimeout(timer.current);
    setHover(null);
  };
  useEffect(() => () => {
    if (timer.current) clearTimeout(timer.current);
  }, []);

  // The app menu closes on a click elsewhere or Escape.
  useEffect(() => {
    if (!menu) return;
    const onDown = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) setMenu(null);
    };
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.key === "Escape") setMenu(null);
    };
    window.addEventListener("mousedown", onDown);
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("mousedown", onDown);
      window.removeEventListener("keydown", onKey);
    };
  }, [menu]);

  const add = (def: NodeDefinition) => {
    if (readonly) return;
    onAdd(def);
    hideHover();
    setMenu(null);
  };

  const onSearchKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "ArrowDown" || e.key === "ArrowUp" || e.key === "ArrowRight" || e.key === "ArrowLeft") {
      if (!searching || tiles.length === 0) return;
      e.preventDefault();
      const forward = e.key === "ArrowDown" || e.key === "ArrowRight";
      setActive((activeIndex + (forward ? 1 : tiles.length - 1)) % tiles.length);
    } else if (e.key === "Enter") {
      e.preventDefault();
      const t = searching ? tiles[activeIndex] : undefined;
      if (t?.kind === "node") add(t.def);
    } else if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      if (filter) setFilter("");
      else e.currentTarget.blur();
    }
  };

  const dragProps = (def: NodeDefinition) => ({
    draggable: !readonly,
    onDragStart: (e: DragEvent) => {
      hideHover();
      setMenu(null);
      e.dataTransfer.setData(NODE_DRAG_TYPE, def.type);
      e.dataTransfer.effectAllowed = "copy";
    },
  });

  const renderTile = (t: Tile, i: number) => {
    if (t.kind === "soon") {
      return (
        <div key={t.key} className="palette-tile soon" data-testid={`palette-soon-${t.integrationId}`} aria-disabled
          title={`${t.label} is coming soon`}>
          <span className="tile-icon"><NodeIcon integrationId={t.integrationId} size={22} /></span>
          <span className="tile-label">{t.label}</span>
          <span className="tile-soon">Coming soon</span>
        </div>
      );
    }
    if (t.kind === "app") {
      return (
        <button
          key={t.key} type="button" className="palette-tile app" data-testid={`palette-app-${t.integrationId}`}
          aria-haspopup="menu" aria-expanded={menu?.tile.key === t.key} disabled={readonly}
          title={`${t.label}: ${t.actions.length} action${t.actions.length === 1 ? "" : "s"}. Click to choose, or drag to add.`}
          {...dragProps(t.actions[0])}
          onClick={(e) => {
            hideHover();
            setMenu(menu?.tile.key === t.key ? null : { tile: t, anchor: rectOf(e.currentTarget) });
          }}
        >
          <span className="tile-icon"><NodeIcon integrationId={t.integrationId} size={22} /></span>
          <span className="tile-label">{t.label}</span>
        </button>
      );
    }
    const def = t.def;
    return (
      <button
        key={t.key} type="button" id={`palette-opt-${def.type}`}
        className={`palette-tile ${searching && i === activeIndex ? "active" : ""}`}
        data-testid={`palette-${def.type}`} disabled={readonly}
        role={searching ? "option" : undefined} aria-selected={searching ? i === activeIndex : undefined}
        aria-label={`Add ${t.label}`}
        {...dragProps(def)}
        onMouseEnter={(e) => {
          const r = rectOf(e.currentTarget);
          if (timer.current) clearTimeout(timer.current);
          timer.current = setTimeout(() => setHover({ def, anchor: r }), HOVER_DELAY_MS);
        }}
        onMouseLeave={hideHover}
        onClick={() => add(def)}
      >
        <span className="tile-icon"><NodeIcon def={def} size={22} /></span>
        <span className="tile-label">{t.label}</span>
      </button>
    );
  };

  return (
    <div className="palette-bar" aria-label="Add steps" data-testid="palette-bar">
      <div className="palette-bar-top">
        <div className="palette-tabs" role="tablist" aria-label="Kinds of steps">
          {PALETTE_TABS.filter((t) => counts.get(t.id) || t.id === "apps").map((t) => (
            <button
              key={t.id} type="button" role="tab" aria-selected={!searching && tab === t.id} title={t.hint}
              className={`palette-tab ${!searching && tab === t.id ? "selected" : ""}`} data-testid={`palette-tab-${t.id}`}
              onClick={() => {
                setTab(t.id);
                setFilter("");
                setMenu(null);
              }}
            >
              {t.label}
            </button>
          ))}
        </div>
        <span className="spacer" />
        <input
          type="search" className="palette-search" placeholder="Search steps…" aria-label="Search nodes"
          aria-controls="palette-results"
          aria-activedescendant={searching && tiles[activeIndex]?.kind === "node" ? `palette-opt-${tiles[activeIndex].key}` : undefined}
          data-palette-search value={filter}
          onChange={(e) => {
            setFilter(e.target.value);
            setActive(0);
            setMenu(null);
          }}
          onKeyDown={onSearchKey}
        />
      </div>
      <div className="palette-row" ref={rowRef} id="palette-results" role={searching ? "listbox" : undefined}
        aria-label={searching ? "Matching steps" : undefined}>
        {searching && (
          <div className="palette-count" role="status" data-testid="palette-count">
            {tiles.length === 0 ? `No steps match “${filter}”.` : `${tiles.length} node${tiles.length === 1 ? "" : "s"}`}
          </div>
        )}
        {tiles.map(renderTile)}
        {catalogList.length === 0 && <p className="muted">The node catalog is empty.</p>}
      </div>
      {menu && (
        <div
          ref={menuRef} className="palette-menu" role="menu" data-testid={`palette-menu-${menu.tile.integrationId}`}
          style={{ left: menu.anchor.left, top: menu.anchor.bottom + 6 }}
        >
          <div className="palette-menu-title">What should {menu.tile.label} do?</div>
          {menu.tile.actions.map((d) => (
            <button key={d.type} type="button" role="menuitem" className="palette-menu-item" data-testid={`palette-${d.type}`}
              {...dragProps(d)} onClick={() => add(d)}>
              <span className="palette-menu-name">{actionName(d)}</span>
              {d.description && <span className="palette-menu-desc">{d.description}</span>}
            </button>
          ))}
        </div>
      )}
      {hover && (
        <Popover anchor={hover.anchor} testId="palette-hover">
          <NodeInfoCard def={hover.def} />
        </Popover>
      )}
    </div>
  );
}
