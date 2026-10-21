import type { Metadata } from "next";
import { Suspense } from "react";
import { LoginForm } from "@/components/login-form";

export const metadata: Metadata = {
  title: "Staff sign-in",
  description: "Sign in to Motivra Command — the staff-only internal operations console.",
};

export default function LoginPage() {
  return (
    <main
      id="main-content"
      className="flex min-h-screen flex-col items-center justify-center bg-slate-100 px-4 py-16"
    >
      <section className="w-full max-w-md rounded-lg border border-slate-200 bg-white p-8 shadow-sm">
        <p className="text-sm font-semibold uppercase tracking-widest text-amber-700">
          Motivra Command
        </p>
        <h1 className="mt-2 text-2xl font-bold tracking-tight text-slate-900">Staff sign-in</h1>
        <p className="mt-2 text-sm leading-relaxed">
          Internal operations console — live job board, escalations queue and system health.
          Access is deny-by-default: only Motivra staff roles (DISPATCHER, ADMIN, SUPER_ADMIN) are
          admitted, and every sign-in is audited.
        </p>
        <Suspense fallback={<p className="mt-6 text-sm text-slate-500">Loading sign-in…</p>}>
          <LoginForm />
        </Suspense>
        <p className="mt-6 border-t border-slate-200 pt-4 text-xs leading-relaxed text-slate-600">
          Authentication runs against the Motivra identity service (POST /v1/auth/login). Tokens
          are short-lived, held in httpOnly cookies, and never exposed to browser code. Customer
          and technician identifiers are masked on screen per docs/PII-HANDLING.md.
        </p>
      </section>
    </main>
  );
}
