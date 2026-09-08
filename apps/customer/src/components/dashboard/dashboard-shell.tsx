"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

/*
 * App shell for the authenticated area (foundation build).
 * Sidebar items that have no backing page yet are rendered as disabled
 * entries with a "soon" badge — honest, not broken links.
 */

type NavItem =
  | { label: string; ready: true; href: string }
  | { label: string; ready: false };

const PRIMARY_NAV: readonly NavItem[] = [
  { label: "Dashboard", ready: true, href: "/dashboard" },
  { label: "Vehicles", ready: true, href: "/dashboard/vehicles" },
  { label: "Jobs", ready: false },
  { label: "Passport", ready: false },
  { label: "Settings", ready: false },
];

export function DashboardShell({ children }: { children: React.ReactNode }) {
  const pathname = usePathname() ?? "";

  return (
    <div className="min-h-screen bg-slate-100">
      <header className="bg-slate-950 text-slate-200">
        <div className="mx-auto flex max-w-7xl flex-wrap items-center justify-between gap-3 px-4 py-3 sm:px-6">
          <Link
            href="/"
            className="text-lg font-bold tracking-tight text-white"
          >
            Motivra <span className="text-amber-400">Drive</span>
          </Link>
          <div className="flex flex-wrap items-center gap-3">
            <p className="rounded-full border border-slate-700 px-3 py-1 text-xs font-semibold text-slate-300">
              API integration pending — UI foundation
            </p>
            <Link
              href="/login"
              className="rounded-md border border-slate-700 px-3 py-1.5 text-sm font-semibold text-white hover:border-amber-400 hover:text-amber-300"
            >
              Sign in
            </Link>
          </div>
        </div>
      </header>

      <div className="mx-auto flex max-w-7xl flex-col gap-6 px-4 py-6 sm:px-6 md:flex-row">
        <aside className="md:w-56 md:shrink-0">
          <nav aria-label="Dashboard">
            <ul className="space-y-1 rounded-lg border border-slate-200 bg-white p-2">
              {PRIMARY_NAV.map((item) => (
                <li key={item.label}>
                  {item.ready ? (
                    <Link
                      href={item.href}
                      aria-current={pathname === item.href ? "page" : undefined}
                      className={
                        pathname === item.href
                          ? "block rounded-md bg-slate-900 px-3 py-2 text-sm font-semibold text-amber-400"
                          : "block rounded-md px-3 py-2 text-sm font-medium text-slate-700 hover:bg-slate-100 hover:text-slate-900"
                      }
                    >
                      {item.label}
                    </Link>
                  ) : (
                    <span
                      aria-disabled="true"
                      title="Planned — ships with the API integration"
                      className="flex cursor-not-allowed items-center justify-between rounded-md px-3 py-2 text-sm font-medium text-slate-400"
                    >
                      {item.label}
                      <span className="rounded-full bg-slate-100 px-2 py-0.5 text-xs font-semibold text-slate-500">
                        soon
                      </span>
                    </span>
                  )}
                </li>
              ))}
            </ul>
          </nav>
        </aside>

        <main id="main-content" className="min-w-0 flex-1">
          {children}
        </main>
      </div>
    </div>
  );
}
