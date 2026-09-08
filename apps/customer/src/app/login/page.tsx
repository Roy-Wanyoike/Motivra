import type { Metadata } from "next";
import Link from "next/link";
import { LoginForm } from "@/components/login/login-form";

export const metadata: Metadata = {
  title: "Sign in",
  description:
    "Sign in to Motivra Drive — your vehicles, service jobs and Vehicle Passport.",
};

export default function LoginPage() {
  return (
    <main
      id="main-content"
      className="flex min-h-screen items-center justify-center bg-slate-100 px-4 py-16"
    >
      <section className="w-full max-w-md rounded-lg border border-slate-200 bg-white p-8 shadow-sm">
        <p className="text-sm font-semibold uppercase tracking-widest text-amber-700">
          Motivra Drive
        </p>
        <h1 className="mt-2 text-2xl font-bold tracking-tight text-slate-900">
          Sign in
        </h1>
        <p className="mt-2 text-sm leading-relaxed">
          Access your vehicles, service jobs and Vehicle Passport.
        </p>
        <LoginForm />
        <p className="mt-6 border-t border-slate-200 pt-4 text-xs leading-relaxed text-slate-600">
          New to Motivra? Account registration (POST /v1/auth/register on the identity service)
          ships together with the login integration. This form is a foundation scaffold — see the{" "}
          <Link
            className="font-semibold text-amber-800 underline underline-offset-4"
            href="/"
          >
            honest status
          </Link>{" "}
          on the landing page.
        </p>
      </section>
    </main>
  );
}
