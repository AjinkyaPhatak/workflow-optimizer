// Icons for nodes and apps (Phase H): brand logos for app integrations
// (from simple-icons, CC0), and simple line icons for everything else.

import {
  siAirtable, siDiscord, siGmail, siGooglecalendar, siGoogledocs, siGoogledrive, siGoogleforms, siGooglemeet, siGooglesheets,
  siNotion, siTelegram, siTrello, siWhatsapp,
} from "simple-icons";
import type { NodeDefinition } from "@/types/api";

interface Brand {
  path: string;
  hex: string;
}

/** Brand logos by integration ID. */
const BRANDS: Record<string, Brand> = {
  gmail: siGmail, google_sheets: siGooglesheets, google_calendar: siGooglecalendar, google_meet: siGooglemeet,
  google_docs: siGoogledocs, google_forms: siGoogleforms, google_drive: siGoogledrive, notion: siNotion, discord: siDiscord,
  airtable: siAirtable, whatsapp: siWhatsapp, telegram: siTelegram, trello: siTrello,
};

/** Line icons (24×24, stroked) by name. */
const GLYPHS: Record<string, string> = {
  input: "M3 12h11M10 8l4 4-4 4M15 4h4a1 1 0 011 1v14a1 1 0 01-1 1h-4",
  output: "M10 12h11M17 8l4 4-4 4M9 4H5a1 1 0 00-1 1v14a1 1 0 001 1h4",
  text: "M5 6h14M12 6v13M9 19h6",
  ai: "M8 3v3M16 3v3M8 18v3M16 18v3M3 8h3M3 16h3M18 8h3M18 16h3M6 6h12v12H6zM10 10h4v4h-4z",
  knowledge: "M4 5.5A2.5 2.5 0 016.5 3H20v15H6.5A2.5 2.5 0 004 20.5zM4 20.5V5.5M8 7h8",
  file: "M6 3h8l4 4v14H6zM14 3v4h4M9 13h6M9 17h6",
  mic: "M12 3a3 3 0 013 3v6a3 3 0 01-6 0V6a3 3 0 013-3zM5 11a7 7 0 0014 0M12 18v3",
  branch: "M6 4v16M6 12h5l7-7M11 12l7 7",
  merge: "M6 4l6 8v8M18 4l-6 8",
  braces: "M9 4H8a2 2 0 00-2 2v3l-2 3 2 3v3a2 2 0 002 2h1M15 4h1a2 2 0 012 2v3l2 3-2 3v3a2 2 0 01-2 2h-1",
  globe: "M12 3a9 9 0 100 18 9 9 0 000-18zM3 12h18M12 3c3 3 3 15 0 18M12 3c-3 3-3 15 0 18",
  plug: "M9 3v5M15 3v5M7 8h10v4a5 5 0 01-10 0zM12 17v4",
  loop: "M5 8h12l-3-3M19 16H7l3 3M19 8v4M5 16v-4",
  clock: "M12 3a9 9 0 100 18 9 9 0 000-18zM12 7v5l3 2",
  ask: "M8 8a4 4 0 118 0c0 3-4 3-4 6M12 18v1",
};

const GLYPH_BY_TYPE: Record<string, string> = {
  input: "input", output: "output", text: "text", llm: "ai", prompt: "ai", structured_output: "ai",
  knowledge_search: "knowledge", knowledge_add: "knowledge", extract_text: "file", transcribe_audio: "mic",
  condition: "branch", switch: "branch", merge: "merge", transform: "braces", json: "braces", http: "globe",
  branch: "branch", map: "loop", repeat: "loop", parallel: "merge", ask: "ask", wait: "clock", delay: "clock", workflow: "braces",
  "ai.write": "ai", "table.extract": "ai", "decision.classify": "branch", "web.search": "globe", "web.read": "globe",
  "analysis.python": "braces", "analysis.describe": "braces", "table.csv": "file", "artifact.save": "file", "artifact.text": "file",
};

/** The brand of an integration ID, if it has a logo. */
export function brandOf(integrationId: string | undefined): Brand | undefined {
  return integrationId ? BRANDS[integrationId] : undefined;
}

function glyphOf(def: Pick<NodeDefinition, "type" | "category">): string {
  return GLYPH_BY_TYPE[def.type] ?? (def.category === "ai" || def.category === "llm" ? "ai" : def.category === "knowledge_base" ? "knowledge"
    : def.category === "multimodal" ? "mic" : "plug");
}

/** A node's icon: its app's logo, else a line icon for what it does. */
export function NodeIcon({ def, integrationId, size = 20 }: {
  def?: Pick<NodeDefinition, "type" | "category" | "integration">;
  integrationId?: string;
  size?: number;
}) {
  const brand = brandOf(integrationId ?? def?.integration?.id);
  if (brand) {
    return (
      <svg width={size} height={size} viewBox="0 0 24 24" role="img" aria-hidden className="brand-icon">
        <path d={brand.path} fill={`#${brand.hex}`} />
      </svg>
    );
  }
  const glyph = def ? glyphOf(def) : "plug";
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" aria-hidden className="glyph-icon" fill="none" stroke="currentColor"
      strokeWidth={1.8} strokeLinecap="round" strokeLinejoin="round">
      <path d={GLYPHS[glyph]} />
    </svg>
  );
}
