"use client";

import { useRouter } from "next/navigation";
import { useEffect, useState, type FormEvent } from "react";
import { BrandMark, Spinner } from "@/components/ui/BrandMark";
import { ErrorBanner } from "@/components/ui/ErrorBanner";
import { useSession } from "@/lib/auth/session";

export default function LoginPage() {
  const router = useRouter();
  const { session, hydrated, hydrate, login, register } = useSession();
  const [mode, setMode] = useState<"login" | "register">("login");
  const [email, setEmail] = useState("");
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [showPassword, setShowPassword] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!hydrated) hydrate();
    else if (session) router.replace("/workflows");
  }, [hydrated, hydrate, session, router]);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      if (mode === "login") await login(email, password);
      else await register(email, name, password);
      router.replace("/workflows");
    } catch (err) {
      setError(err);
      setBusy(false);
    }
  }

  const registering = mode === "register";

  return (
    <main className="auth-page">
      <section className="auth-brand" aria-hidden>
        <div className="auth-brand-inner">
          <div className="auth-logo">
            <BrandMark size={28} />
            <span>Workflow Optimizer</span>
          </div>
          <h2>Design AI workflows visually.</h2>
          <p>Connect inputs, models and integrations on a canvas, then run and debug every step.</p>
          <div className="auth-illustration">
            <div className="auth-node">Input</div>
            <div className="auth-edge" />
            <div className="auth-node accent">LLM</div>
            <div className="auth-edge" />
            <div className="auth-node">Output</div>
          </div>
        </div>
      </section>

      <section className="auth-panel">
        <form className="auth-form" onSubmit={submit} aria-busy={busy}>
          <div className="auth-logo compact">
            <BrandMark size={24} />
            <span>Workflow Optimizer</span>
          </div>
          <div>
            <h1>{registering ? "Create your account" : "Sign in"}</h1>
            <p className="muted">{registering ? "Start building workflows in minutes." : "Welcome back. Sign in to continue to your workflows."}</p>
          </div>
          <ErrorBanner error={error} />
          {registering && (
            <div className="form-field">
              <label htmlFor="name">Name</label>
              <input id="name" value={name} onChange={(e) => setName(e.target.value)} required autoComplete="name" disabled={busy} />
            </div>
          )}
          <div className="form-field">
            <label htmlFor="email">Email</label>
            <input
              id="email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} required autoComplete="email"
              placeholder="you@company.com" disabled={busy} autoFocus
            />
          </div>
          <div className="form-field">
            <label htmlFor="password">Password</label>
            <div className="password-input">
              <input
                id="password"
                type={showPassword ? "text" : "password"}
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
                minLength={registering ? 8 : undefined}
                autoComplete={registering ? "new-password" : "current-password"}
                aria-describedby={registering ? "password-help" : undefined}
                disabled={busy}
              />
              <button
                type="button"
                className="password-toggle"
                aria-controls="password"
                aria-pressed={showPassword}
                onClick={() => setShowPassword((v) => !v)}
              >
                {showPassword ? "Hide" : "Show"}
              </button>
            </div>
            {registering && <div className="hint" id="password-help">At least 8 characters.</div>}
          </div>
          <button className="primary large" type="submit" disabled={busy}>
            {busy && <Spinner />}
            {busy ? (registering ? "Creating account…" : "Signing in…") : registering ? "Create account" : "Sign in"}
          </button>
          <p className="auth-switch muted">
            {registering ? "Already have an account?" : "New to Workflow Optimizer?"}{" "}
            <button
              type="button"
              className="link"
              onClick={() => {
                setMode(registering ? "login" : "register");
                setError(null);
              }}
            >
              {registering ? "Sign in" : "Create an account"}
            </button>
          </p>
        </form>
      </section>
    </main>
  );
}
