import type { Metadata } from "next";
import { Suspense } from "react";
import { LoginForm } from "@/components/login-form";

export const metadata: Metadata = {
  title: "Garage staff sign-in",
  description: "Sign in to Motivra Garage — the partner garage console.",
};

export default function LoginPage() {
  return (
    <main
      id="main-content"
      className="flex min-h-screen flex-col items-center justify-center bg-slate-100 px-4 py-16"
    >
      <section className="w-full max-w-md rounded-lg border border-slate-200 bg-white p-8 shadow-sm">
        <p className="text-sm font-semibold uppercase tracking-widest text-amber-700">
          Motivra Garage
        </p>
        <h1 className="mt-2 text-2xl font-bold tracking-tight text-slate-900">Garage staff sign-in</h1>
        <p className="mt-2 text-sm leading-relaxed">
          Partner garage console — appointments, capacity, the garage job board and invoices.
          Access is deny-by-default: only accounts with the GARAGE role are admitted, every
          console view is scoped to your own garage via your sign-in, and every sign-in is
          audited.
        </p>
        <Suspense fallback={<p className="mt-6 text-sm text-slate-500">Loading sign-in…</p>}>
          <LoginForm />
        </Suspense>
        <p className="mt-6 border-t border-slate-200 pt-4 text-xs leading-relaxed text-slate-600">
          Authentication runs against the Motivra identity service (POST /v1/auth/login). Tokens
          are short-lived, held in httpOnly cookies, and never exposed to browser code. Customer
          and job identifiers are masked on screen per docs/PII-HANDLING.md.
        </p>
      </section>
    </main>
  );
}
