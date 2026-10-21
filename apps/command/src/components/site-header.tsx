import { maskIdentifier } from "@/lib/pii/mask";
import { LogoutButton } from "@/components/logout-button";

/*
 * Console header: identity of the console, the verified operator (masked),
 * their highest role, and the sign-out control. Rendered only for staff —
 * the proxy and the page guard guarantee that.
 */

export function SiteHeader({
  actorSub,
  role,
  environment,
}: {
  actorSub: string;
  role: string;
  environment: string;
}) {
  return (
    <header className="border-b border-slate-200 bg-white">
      <div className="mx-auto flex w-full max-w-7xl flex-wrap items-center justify-between gap-3 px-4 py-3 sm:px-6">
        <div className="flex items-center gap-3">
          <p className="text-sm font-semibold uppercase tracking-widest text-amber-700">
            Motivra Command
          </p>
          <span className="rounded-full bg-slate-100 px-2 py-0.5 text-xs font-medium text-slate-600">
            {environment}
          </span>
        </div>
        <div className="flex items-center gap-3 text-sm">
          <span className="text-slate-600">
            Signed in as{" "}
            <span className="font-mono text-xs text-slate-500">
              {maskIdentifier(actorSub, "staff")}
            </span>
          </span>
          <span className="rounded-full bg-teal-50 px-2 py-0.5 text-xs font-semibold text-teal-800 ring-1 ring-inset ring-teal-300">
            {role === "" ? "staff" : role}
          </span>
          <LogoutButton />
        </div>
      </div>
    </header>
  );
}
