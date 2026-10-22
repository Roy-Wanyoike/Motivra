import { maskGarageId, maskIdentifier } from "@/lib/pii/mask";
import { LogoutButton } from "@/components/logout-button";

/*
 * Console header: identity of the console, the verified staff member
 * (masked), their highest role, the garage they are operating (masked,
 * from the token's tenant claim — never typed in) and the sign-out
 * control. Rendered only for garage staff — the proxy and the page guard
 * guarantee that.
 */

export function SiteHeader({
  actorSub,
  role,
  garageTenantId,
  environment,
}: {
  actorSub: string;
  role: string;
  garageTenantId: string;
  environment: string;
}) {
  return (
    <header className="border-b border-slate-200 bg-white">
      <div className="mx-auto flex w-full max-w-7xl flex-wrap items-center justify-between gap-3 px-4 py-3 sm:px-6">
        <div className="flex items-center gap-3">
          <p className="text-sm font-semibold uppercase tracking-widest text-amber-700">
            Motivra Garage
          </p>
          <span className="rounded-full bg-slate-100 px-2 py-0.5 text-xs font-medium text-slate-600">
            {environment}
          </span>
        </div>
        <div className="flex flex-wrap items-center gap-3 text-sm">
          {garageTenantId === "" ? (
            <span
              data-testid="garage-binding"
              className="rounded-full bg-amber-50 px-2 py-0.5 text-xs font-semibold text-amber-800 ring-1 ring-inset ring-amber-300"
            >
              No garage bound to this account
            </span>
          ) : (
            <span
              data-testid="garage-binding"
              className="rounded-full bg-teal-50 px-2 py-0.5 text-xs font-semibold text-teal-800 ring-1 ring-inset ring-teal-300"
              title="The garage this session is scoped to — taken from your sign-in, never typed in"
            >
              Garage {maskGarageId(garageTenantId)}
            </span>
          )}
          <span className="text-slate-600">
            Signed in as{" "}
            <span className="font-mono text-xs text-slate-500">
              {maskIdentifier(actorSub, "staff")}
            </span>
          </span>
          <span className="rounded-full bg-amber-50 px-2 py-0.5 text-xs font-semibold text-amber-800 ring-1 ring-inset ring-amber-300">
            {role === "" ? "garage staff" : role}
          </span>
          <LogoutButton />
        </div>
      </div>
    </header>
  );
}
