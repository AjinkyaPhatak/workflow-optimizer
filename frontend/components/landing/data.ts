// Content for the public landing page. Every workflow named here is a real
// template in the gallery; apps marked soon are shown as Coming soon.

import {
  siAirtable, siDiscord, siGmail, siGooglecalendar, siGoogledocs, siGoogleforms, siGooglegemini, siGooglemeet,
  siGooglesheets, siNotion,
} from "simple-icons";

export interface Brand {
  title: string;
  path: string;
  hex: string;
}

export const APPS = {
  gmail: siGmail,
  sheets: siGooglesheets,
  calendar: siGooglecalendar,
  meet: siGooglemeet,
  docs: siGoogledocs,
  forms: siGoogleforms,
  gemini: siGooglegemini,
  notion: siNotion,
  airtable: siAirtable,
  discord: siDiscord,
} satisfies Record<string, Brand>;

export type AppKey = keyof typeof APPS;

/** The logo wall: name, app and whether it is still coming soon. */
export const APP_WALL: { name: string; app: AppKey; soon?: boolean }[] = [
  { name: "Gmail", app: "gmail" },
  { name: "Google Sheets", app: "sheets" },
  { name: "Google Calendar", app: "calendar" },
  { name: "Google Meet", app: "meet" },
  { name: "Google Docs", app: "docs" },
  { name: "Google Forms", app: "forms" },
  { name: "Gemini AI", app: "gemini" },
  { name: "Notion", app: "notion", soon: true },
  { name: "Airtable", app: "airtable", soon: true },
  { name: "Discord", app: "discord", soon: true },
];

/** Words the hero headline cycles through: "Automate any ___". */
export const HERO_WORDS = ["inbox", "meeting", "report", "follow-up", "spreadsheet", "busywork"];

/** Cards on the hero's curved carousel. */
export type ArcCard =
  | { kind: "app"; app: AppKey; label: string; tone: string }
  | { kind: "note"; title: string; lines: string[]; tone: string }
  | { kind: "flow"; steps: AppKey[]; label: string; tone: string }
  | { kind: "stat"; big: string; label: string; tone: string };

export const ARC_CARDS: ArcCard[] = [
  { kind: "app", app: "gmail", label: "Summarize my unread email", tone: "t-peach" },
  { kind: "note", title: "Inbox digest", lines: ["3 emails need a reply", "Invoice from Acme is due Friday", "Team lunch moved to 1 pm"], tone: "t-paper" },
  { kind: "flow", steps: ["calendar", "gemini", "docs"], label: "Brief me on today's meetings", tone: "t-ink" },
  { kind: "app", app: "sheets", label: "Weekly report from a sheet", tone: "t-sun" },
  { kind: "stat", big: "26", label: "ready-made templates", tone: "t-teal" },
  { kind: "app", app: "meet", label: "Schedule a meeting with a Meet link", tone: "t-charcoal" },
  { kind: "note", title: "Draft reply", lines: ["Hi Sam,", "Thanks for the update. Thursday at 3 works for me.", "Best, Alex"], tone: "t-paper" },
  { kind: "flow", steps: ["forms", "gemini", "gmail"], label: "Thank people who fill in a form", tone: "t-ink" },
  { kind: "app", app: "docs", label: "Prepare a company brief", tone: "t-sandy" },
  { kind: "stat", big: "0", label: "lines of code", tone: "t-sun" },
  { kind: "app", app: "gemini", label: "Free AI with Gemini", tone: "t-teal" },
  { kind: "note", title: "Meeting notes", lines: ["Decisions: ship on Monday", "Action: Priya sends the deck", "Open: pricing page copy"], tone: "t-paper" },
  { kind: "flow", steps: ["gmail", "gemini", "sheets"], label: "Log receipts from email in a sheet", tone: "t-ink" },
  { kind: "app", app: "forms", label: "Copy form responses to a sheet", tone: "t-peach" },
  { kind: "app", app: "calendar", label: "Today's agenda", tone: "t-charcoal" },
];

/** The sticky showcase: one scenario per tab. */
export interface Scenario {
  tab: string;
  prompt: string;
  result: { title: string; lines: string[] };
  nodes: { app: AppKey | "input" | "output" | "mic"; label: string; tag: string; x: number; y: number }[];
  wires: [number, number][];
}

export const SCENARIOS: Scenario[] = [
  {
    tab: "Email",
    prompt: "Summarize my unread email and tell me what needs a reply",
    result: { title: "Inbox digest", lines: ["3 emails need a reply today", "Acme invoice due Friday", "Design review moved to 4 pm"] },
    nodes: [
      { app: "gmail", label: "Search unread", tag: "Gmail", x: 6, y: 18 },
      { app: "gemini", label: "Summarize", tag: "AI", x: 40, y: 50 },
      { app: "output", label: "Inbox digest", tag: "Result", x: 72, y: 20 },
    ],
    wires: [[0, 1], [1, 2]],
  },
  {
    tab: "Meetings",
    prompt: "Brief me on today's meetings before my first call",
    result: { title: "Today, 3 meetings", lines: ["10:00 Design review, bring the mockups", "13:00 Lunch with Sam", "16:30 Weekly sync, 2 open items"] },
    nodes: [
      { app: "calendar", label: "Today's events", tag: "Calendar", x: 6, y: 52 },
      { app: "gemini", label: "Write the brief", tag: "AI", x: 38, y: 18 },
      { app: "docs", label: "Save as a doc", tag: "Docs", x: 70, y: 52 },
    ],
    wires: [[0, 1], [1, 2]],
  },
  {
    tab: "Reports",
    prompt: "Turn my sales sheet into a weekly report",
    result: { title: "Week 43 report", lines: ["Revenue up 12% on last week", "Top product: Starter plan", "2 deals waiting on a reply"] },
    nodes: [
      { app: "sheets", label: "Read rows", tag: "Sheets", x: 6, y: 22 },
      { app: "gemini", label: "Find the trends", tag: "AI", x: 38, y: 56 },
      { app: "gmail", label: "Draft to me", tag: "Gmail", x: 70, y: 22 },
    ],
    wires: [[0, 1], [1, 2]],
  },
  {
    tab: "Sales",
    prompt: "When someone fills in my form, thank them and log them in a sheet",
    result: { title: "Thank-you draft", lines: ["Hi Jordan,", "Thanks for signing up for the workshop!", "We'll send details on Monday."] },
    nodes: [
      { app: "forms", label: "New responses", tag: "Forms", x: 6, y: 40 },
      { app: "gemini", label: "Write a thank-you", tag: "AI", x: 38, y: 14 },
      { app: "gmail", label: "Save draft", tag: "Gmail", x: 70, y: 14 },
      { app: "sheets", label: "Add a row", tag: "Sheets", x: 54, y: 66 },
    ],
    wires: [[0, 1], [1, 2], [0, 3]],
  },
  {
    tab: "Content",
    prompt: "Turn my meeting recording into notes in a Google Doc",
    result: { title: "Meeting notes", lines: ["Summary: launch moves to May", "Decision: keep the free plan", "Action: Priya updates the site"] },
    nodes: [
      { app: "mic", label: "Recording", tag: "Upload", x: 6, y: 20 },
      { app: "gemini", label: "Transcribe", tag: "AI", x: 30, y: 58 },
      { app: "gemini", label: "Write notes", tag: "AI", x: 54, y: 18 },
      { app: "docs", label: "Create doc", tag: "Docs", x: 76, y: 58 },
    ],
    wires: [[0, 1], [1, 2], [2, 3]],
  },
];

/** "Built for" tabs: who it helps and which templates they'd start with. */
export interface Persona {
  tab: string;
  title: string;
  text: string;
  motif: string;
  app: AppKey;
  picks: { name: string; app: AppKey }[];
}

export const PERSONAS: Persona[] = [
  {
    tab: "Founders", title: "Founders",
    text: "Start the day knowing what matters. Your inbox, calendar and numbers, summed up before your first coffee.",
    motif: "motif-teal", app: "gmail",
    picks: [
      { name: "Summarize my unread email", app: "gmail" }, { name: "Brief me on today's meetings", app: "calendar" },
      { name: "Weekly report from a sheet", app: "sheets" }, { name: "Prepare a company brief", app: "docs" },
    ],
  },
  {
    tab: "Sales", title: "Sales teams",
    text: "Follow up faster. Every form response logged, every lead thanked, every meeting booked with a link.",
    motif: "motif-peach", app: "forms",
    picks: [
      { name: "Copy form responses to a sheet", app: "sheets" }, { name: "Thank people who fill in a form", app: "forms" },
      { name: "Schedule a meeting with a Meet link", app: "meet" }, { name: "Draft a reply to my latest email", app: "gmail" },
    ],
  },
  {
    tab: "Operations", title: "Operations",
    text: "Take the copy-paste out of the week. Receipts, documents and recordings turned into tidy records.",
    motif: "motif-charcoal", app: "sheets",
    picks: [
      { name: "Log receipts from email in a sheet", app: "sheets" }, { name: "Summarize a document", app: "docs" },
      { name: "Meeting notes from a recording", app: "meet" }, { name: "Ask questions about a Google Doc", app: "docs" },
    ],
  },
  {
    tab: "Creators", title: "Creators",
    text: "Go from idea to draft in one click. Blog posts, social posts and rewrites in the tone you want.",
    motif: "motif-sun", app: "gemini",
    picks: [
      { name: "Write a blog post", app: "gemini" }, { name: "Social media posts from an idea", app: "gemini" },
      { name: "Rewrite text in a different tone", app: "gemini" }, { name: "Translate text", app: "gemini" },
    ],
  },
];

export const FAQ: { q: string; a: string }[] = [
  {
    q: "What is Workflow Optimizer?",
    a: "A visual builder for everyday automations. You connect cards (Gmail, Calendar, Sheets, AI and more) into a workflow, press Run, and watch every step happen.",
  },
  {
    q: "Do I need to know how to code?",
    a: "No. Start from a template, fill in the blanks it asks for, and run it. Everything is point and click, and errors are explained in plain English.",
  },
  {
    q: "Is it free?",
    a: "Yes. The AI steps use Google's free Gemini tier with your own free key, so there's nothing to pay to get started.",
  },
  {
    q: "Which apps does it work with?",
    a: "Gmail, Google Calendar, Google Meet, Google Docs, Google Sheets and Google Forms today, plus AI for writing, summarizing, translating and transcribing. Notion, Airtable and Discord are coming soon.",
  },
  {
    q: "Will it send emails without asking me?",
    a: "No. The email templates save drafts in your Gmail so you can check them first. A workflow only sends email if you add a Send step yourself.",
  },
  {
    q: "Is my data safe?",
    a: "Your keys are encrypted before they are stored, your Google sign-in never reaches the browser, and you can disconnect an account at any time.",
  },
  {
    q: "Can I make my own templates?",
    a: "Yes. Build a workflow once, choose Save as template, and reuse it whenever you like.",
  },
];
