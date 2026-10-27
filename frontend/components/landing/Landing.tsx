"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { BrandMark } from "@/components/ui/BrandMark";
import { SECTIONS } from "@/components/ui/TopBar";
import { useSession } from "@/lib/auth/session";
import { Apps } from "./Apps";
import { Hero } from "./Hero";
import { Faq, Footer, Personas, Steps } from "./Sections";
import { Showcase } from "./Showcase";

function Nav({ signedIn }: { signedIn: boolean }) {
  const [open, setOpen] = useState(false);
  return (
    <header className="lp-nav-wrap">
      <nav className="lp-nav" aria-label="Main">
        <Link href="/" className="lp-logo"><BrandMark size={22} /> Workflow Optimizer</Link>
        <div className={`lp-nav-links${open ? " is-open" : ""}`}>
          {SECTIONS.map((s) => (
            <Link key={s.href} href={s.href} className={s.href === "/" ? "is-on" : undefined} aria-current={s.href === "/" ? "page" : undefined}
              onClick={() => setOpen(false)}>{s.label}</Link>
          ))}
          <a href="#faq" onClick={() => setOpen(false)}>FAQ</a>
        </div>
        <span className="lp-spacer" />
        {signedIn ? (
          <Link className="lp-btn lp-btn-primary lp-btn-sm" href="/workflows">Open app</Link>
        ) : (
          <>
            <Link className="lp-btn lp-btn-sun lp-btn-sm" href="/login">Sign in</Link>
            <Link className="lp-btn lp-btn-primary lp-btn-sm lp-hide-sm" href="/login">Start for free</Link>
          </>
        )}
        <button className="lp-burger" aria-label="Menu" aria-expanded={open} onClick={() => setOpen(!open)}>
          <span /><span />
        </button>
      </nav>
    </header>
  );
}

/** Adds .is-in to [data-reveal] elements as they scroll into view. */
function useReveal() {
  useEffect(() => {
    const els = document.querySelectorAll<HTMLElement>(".lp [data-reveal]");
    const io = new IntersectionObserver(
      (entries) => entries.forEach((e) => {
        if (e.isIntersecting) {
          e.target.classList.add("is-in");
          io.unobserve(e.target);
        }
      }),
      { rootMargin: "0px 0px -8% 0px" },
    );
    els.forEach((el) => io.observe(el));
    return () => io.disconnect();
  }, []);
}

export function Landing({ fontClass }: { fontClass: string }) {
  const { session, hydrated, hydrate } = useSession();
  useEffect(() => {
    if (!hydrated) hydrate();
  }, [hydrated, hydrate]);
  useReveal();
  const signedIn = Boolean(session);

  return (
    <div className={`lp ${fontClass}`}>
      <Nav signedIn={signedIn} />
      <main>
        <Hero signedIn={signedIn} />
        <Apps />
        <Showcase />
        <Personas />
        <div className="lp-dark">
          <Steps />
          <Faq />
          <Footer signedIn={signedIn} />
        </div>
      </main>
    </div>
  );
}
