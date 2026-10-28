"use client";

import { useState, type FormEvent } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { withBasePath } from "@/lib/base-path";

/*
 * Garage staff login form. Credentials go to Garage's own /api/auth/login
 * route handler (server-side), which calls the identity service, verifies
 * the issued token against the JWKS, checks the GARAGE-role allowlist and
 * sets httpOnly cookies. The browser never sees a token, and non-garage
 * callers are denied here with a clear message.
 */

type LoginState =
  | { kind: "idle" }
  | { kind: "busy" }
  | { kind: "invalid-credentials" }
  | { kind: "not-garage-staff" }
  | { kind: "unreachable" }
  | { kind: "error"; message: string };

export function LoginForm() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const [state, setState] = useState<LoginState>({ kind: "idle" });

  async function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    const email = String(data.get("email") ?? "");
    const password = String(data.get("password") ?? "");
    setState({ kind: "busy" });
    try {
      const response = await fetch(withBasePath("/api/auth/login"), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email, password }),
      });
      if (response.ok) {
        const next = searchParams.get("next");
        const target = next !== null && next.startsWith("/") && !next.startsWith("//") ? next : "/";
        router.replace(target);
        router.refresh();
        return;
      }
      if (response.status === 401) {
        setState({ kind: "invalid-credentials" });
        return;
      }
      if (response.status === 403) {
        setState({ kind: "not-garage-staff" });
        return;
      }
      if (response.status === 502 || response.status === 504) {
        setState({ kind: "unreachable" });
        return;
      }
      setState({ kind: "error", message: `Sign-in failed (HTTP ${response.status}).` });
    } catch {
      setState({ kind: "unreachable" });
    }
  }

  const busy = state.kind === "busy";

  return (
    <form onSubmit={onSubmit} className="mt-6 space-y-4" aria-label="Garage staff sign-in">
      <div className="flex flex-col gap-1">
        <label htmlFor="login-email" className="text-sm font-semibold text-slate-700">
          Work email
        </label>
        <input
          id="login-email"
          name="email"
          type="email"
          autoComplete="email"
          required
          maxLength={254}
          className="min-h-11 rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-800"
        />
      </div>
      <div className="flex flex-col gap-1">
        <label htmlFor="login-password" className="text-sm font-semibold text-slate-700">
          Password
        </label>
        <input
          id="login-password"
          name="password"
          type="password"
          autoComplete="current-password"
          required
          maxLength={128}
          className="min-h-11 rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-800"
        />
      </div>

      {state.kind === "invalid-credentials" ? (
        <p role="alert" className="rounded-md border border-red-300 bg-red-50 p-3 text-sm text-red-800">
          Invalid email or password.
        </p>
      ) : null}
      {state.kind === "not-garage-staff" ? (
        <p role="alert" className="rounded-md border border-red-300 bg-red-50 p-3 text-sm text-red-800">
          This account authenticated, but Motivra Garage is for partner garage staff with the
          GARAGE role. Ask a Motivra administrator for a garage role grant.
        </p>
      ) : null}
      {state.kind === "unreachable" ? (
        <p role="alert" className="rounded-md border border-amber-300 bg-amber-50 p-3 text-sm text-amber-900">
          The identity service is unreachable right now. Try again shortly.
        </p>
      ) : null}
      {state.kind === "error" ? (
        <p role="alert" className="rounded-md border border-red-300 bg-red-50 p-3 text-sm text-red-800">
          {state.message}
        </p>
      ) : null}

      <button
        type="submit"
        disabled={busy}
        className="min-h-11 w-full rounded-md bg-amber-700 px-4 py-2 text-sm font-semibold text-white hover:bg-amber-800 disabled:opacity-60"
      >
        {busy ? "Signing in…" : "Sign in"}
      </button>
    </form>
  );
}
