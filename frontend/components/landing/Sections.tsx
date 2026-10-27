"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { BrandMark } from "@/components/ui/BrandMark";
import { AppLogo } from "./Brand";
import { FAQ, PERSONAS } from "./data";

/** "Built for": a centre card with templates floating around it, by audience. */
export function Personas() {
  const [active, setActive] = useState(0);
  const [paused, setPaused] = useState(false);
  const p = PERSONAS[active];

  useEffect(() => {
    if (paused) return;
    const id = window.setTimeout(() => setActive((a) => (a + 1) % PERSONAS.length), 5200);
    return () => clearTimeout(id);
  }, [active, paused]);

  return (
    <section className="lp-personas" onMouseEnter={() => setPaused(true)} onMouseLeave={() => setPaused(false)}>
      <span className="lp-eyebrow" data-reveal>Built for</span>
      <div className="lp-persona-stage" key={active}>
        {p.picks.map((pick, i) => (
          <div key={pick.name} className={`lp-float f-${i}`} style={{ animationDelay: `${i * 90}ms` }}>
            <span className="lp-float-tag">Template</span>
            <span className="lp-float-icon"><AppLogo app={pick.app} size={26} /></span>
            <span className="lp-float-name">{pick.name}</span>
          </div>
        ))}
        <article className="lp-persona-card">
          <div className="lp-persona-art" style={{ background: p.gradient }}>
            <span className="lp-persona-side">WORKFLOW OPTIMIZER</span>
            <span className="lp-persona-logo"><AppLogo app={p.app} size={64} mono="#fff" /></span>
            <span className="lp-persona-rings" aria-hidden />
          </div>
          <h3>{p.title}</h3>
          <p>{p.text}</p>
        </article>
      </div>
      <div className="lp-pills" role="tablist" aria-label="Who it's for">
        {PERSONAS.map((x, i) => (
          <button key={x.tab} role="tab" aria-selected={i === active} className={i === active ? "is-on" : undefined} onClick={() => setActive(i)}>
            {x.tab}
          </button>
        ))}
      </div>
    </section>
  );
}

const STEPS = [
  {
    n: "01", title: "Connect", text: "Sign in with Google and add a free Gemini key.",
    points: ["Gmail, Calendar, Docs, Sheets, Forms, Meet", "Keys are encrypted", "Disconnect any time"],
    cta: "Create a free account", href: "/login",
  },
  {
    n: "02", title: "Pick a template", text: "Start from 26 ready-made workflows.",
    points: ["Email, meetings, reports, writing", "Fill in the blanks it asks for", "Or start from an empty canvas"],
    cta: "Browse templates", href: "/templates",
  },
  {
    n: "03", title: "Run it", text: "Press Run and watch every step happen.",
    points: ["See each card's result", "Errors explained in plain English", "Save your own as templates"],
    cta: "Open my workflows", href: "/workflows",
  },
];

export function Steps() {
  return (
    <section className="lp-steps">
      <h2 className="lp-display" data-reveal>Three steps. Free to start.</h2>
      <div className="lp-step-grid">
        {STEPS.map((s, i) => (
          <article key={s.n} className={`lp-step${i === 0 ? " is-glow" : ""}`} data-reveal style={{ transitionDelay: `${i * 90}ms` }}>
            <span className="lp-step-n">{s.n}</span>
            <h3>{s.title}</h3>
            <p>{s.text}</p>
            <ul>
              {s.points.map((x) => <li key={x}><span aria-hidden>✓</span>{x}</li>)}
            </ul>
            <Link className={`lp-btn ${i === 2 ? "lp-btn-gradient" : "lp-btn-primary"}`} href={s.href}>{s.cta}</Link>
          </article>
        ))}
      </div>
    </section>
  );
}

export function Faq() {
  const [open, setOpen] = useState<number | null>(0);
  return (
    <section className="lp-faq" id="faq">
      <h2 className="lp-display" data-reveal>Frequently asked questions</h2>
      <ul>
        {FAQ.map((f, i) => (
          <li key={f.q} className={open === i ? "is-open" : undefined}>
            <button aria-expanded={open === i} onClick={() => setOpen(open === i ? null : i)}>
              {f.q}
              <span className="lp-plus" aria-hidden />
            </button>
            <div className="lp-faq-a"><p>{f.a}</p></div>
          </li>
        ))}
      </ul>
    </section>
  );
}

export function Footer({ signedIn }: { signedIn: boolean }) {
  return (
    <footer className="lp-footer">
      <div className="lp-wordmark" aria-hidden><BrandMark size={120} /> Workflow</div>
      <div className="lp-foot-card">
        <div className="lp-foot-cta">
          <h3>Ready when you are</h3>
          <p>Pick a template, fill in the blanks, press Run.</p>
          <Link className="lp-btn lp-btn-primary" href={signedIn ? "/workflows" : "/login"}>
            {signedIn ? "Open my workflows" : "Get started for free"}
          </Link>
        </div>
        <nav className="lp-foot-links" aria-label="Footer">
          <div>
            <b>Product</b>
            <Link href="/templates">Templates</Link>
            <Link href="/workflows">Workflows</Link>
            <a href="#how">How it works</a>
          </div>
          <div>
            <b>Account</b>
            <Link href="/login">Sign in</Link>
            <Link href="/settings/connected-accounts">Connected apps</Link>
            <Link href="/settings/usage">Usage</Link>
          </div>
          <div>
            <b>Help</b>
            <a href="#faq">FAQ</a>
            <Link href="/settings/credentials">API keys</Link>
          </div>
        </nav>
        <div className="lp-foot-base">
          <span><span className="lp-dot" /> Free AI with Gemini</span>
          <span>© {new Date().getFullYear()} Workflow Optimizer</span>
        </div>
      </div>
    </footer>
  );
}
