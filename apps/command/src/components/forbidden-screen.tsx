/*
 * In-page forbidden screen — defense in depth: the proxy already answers
 * real 403s to non-staff callers; if a non-staff session ever reaches a
 * page render, this screen shows instead of console data.
 */
export function ForbiddenScreen() {
  return (
    <main
      id="main-content"
      className="flex min-h-screen flex-col items-center justify-center px-4 py-16"
    >
      <section className="w-full max-w-md rounded-lg border border-slate-200 bg-white p-8 shadow-sm">
        <p className="text-sm font-semibold uppercase tracking-widest text-amber-700">
          Motivra Command
        </p>
        <h1 className="mt-2 text-2xl font-bold tracking-tight text-slate-900">
          403 — Not authorized
        </h1>
        <p className="mt-3 text-sm leading-relaxed">
          Your account authenticated successfully, but Motivra Command is the staff-only internal
          operations console and your roles do not include a Command staff role.
        </p>
        <p className="mt-2 text-sm leading-relaxed">
          Staff roles today: <code className="rounded bg-slate-100 px-1">DISPATCHER</code>,{" "}
          <code className="rounded bg-slate-100 px-1">ADMIN</code>,{" "}
          <code className="rounded bg-slate-100 px-1">SUPER_ADMIN</code>. Role grants live in the
          identity service — this console cannot widen them.
        </p>
      </section>
    </main>
  );
}
