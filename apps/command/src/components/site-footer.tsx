import Link from "next/link";

/*
 * Console footer. The root wrapper is min-h-screen flex flex-col with
 * mt-auto here, so the footer sticks to the viewport bottom on short
 * renders and pushes down naturally when panels overflow.
 */
export function SiteFooter() {
  return (
    <footer className="mt-auto border-t border-slate-200 bg-white">
      <div className="mx-auto flex w-full max-w-7xl flex-wrap items-center justify-between gap-2 px-4 py-4 text-xs text-slate-500 sm:px-6">
        <p>
          Motivra Command — internal operations. Staff-only ({`DISPATCHER / ADMIN / SUPER_ADMIN`});
          every other role gets 403.
        </p>
        <p>
          Identifiers masked per the{" "}
          <Link
            href="https://github.com/Roy-Wanyoike/Motivra/blob/main/docs/PII-HANDLING.md"
            className="font-semibold text-amber-800 underline underline-offset-4"
            rel="noreferrer"
          >
            PII handling policy
          </Link>{" "}
          · views are audit-logged.
        </p>
      </div>
    </footer>
  );
}
