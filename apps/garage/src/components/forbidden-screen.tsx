/*
 * In-page forbidden screen — defense in depth: the proxy already answers
 * real 403s to non-garage callers; if a non-garage session ever reaches a
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
          Motivra Garage
        </p>
        <h1 className="mt-2 text-2xl font-bold tracking-tight text-slate-900">
          403 — Not authorized
        </h1>
        <p className="mt-3 text-sm leading-relaxed">
          Your account authenticated successfully, but Motivra Garage is the partner garage
          console and your roles do not include the GARAGE role.
        </p>
        <p className="mt-2 text-sm leading-relaxed">
          The garage console admits exactly one role:{" "}
          <code className="rounded bg-slate-100 px-1">GARAGE</code>. Motivra operators use the
          Command console. Role grants live in the identity service — this console cannot widen
          them.
        </p>
      </section>
    </main>
  );
}
