import type { Metadata } from "next";
import Link from "next/link";
import { VEHICLE_ENDPOINTS } from "@/lib/api/vehicles";

export const metadata: Metadata = {
  title: "Vehicles",
};

const TABLE_HEADERS = [
  "VIN",
  "Make",
  "Model",
  "Year",
  "Latest odometer (km)",
  "Registered",
  "Passport",
] as const;

export default function VehiclesPage() {
  return (
    <>
      <h1 className="text-2xl font-bold tracking-tight text-slate-900">Vehicles</h1>
      <p className="mt-2 max-w-3xl text-sm leading-relaxed">
        The Vehicle Registry is the canonical identity system for vehicles — one vehicle, one
        identity. This table will list the caller&apos;s vehicles once a registry listing endpoint
        ships; registration uses{" "}
        <code className="rounded bg-slate-100 px-1.5 py-0.5">{VEHICLE_ENDPOINTS.register}</code>{" "}
        and per-vehicle reads use{" "}
        <code className="rounded bg-slate-100 px-1.5 py-0.5">{VEHICLE_ENDPOINTS.get}</code> today.
      </p>

      <div className="mt-6 overflow-x-auto rounded-lg border border-slate-200 bg-white">
        <table className="w-full min-w-[42rem] text-left text-sm">
          <caption className="sr-only">
            Registered vehicles — populated from the vehicles API once connected
          </caption>
          <thead className="bg-slate-100 text-slate-900">
            <tr>
              {TABLE_HEADERS.map((header) => (
                <th key={header} scope="col" className="px-4 py-3 font-semibold">
                  {header}
                </th>
              ))}
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-200">
            <tr>
              <td colSpan={TABLE_HEADERS.length} className="px-4 py-10 text-center text-sm text-slate-600">
                No vehicles to show yet — the vehicles API is not connected. When it is, each row
                will link to the vehicle’s passport timeline.
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <section
        aria-labelledby="passport-preview-heading"
        className="mt-6 rounded-lg border border-slate-200 bg-white p-6"
      >
        <h2 id="passport-preview-heading" className="text-base font-semibold text-slate-900">
          Vehicle Passport timeline
        </h2>
        <p className="mt-2 max-w-3xl text-sm leading-relaxed">
          Every vehicle gets a read-only passport assembled from its append-only history —
          corrections are new records, never edits. The timeline placeholder component is already
          in place at a sample route; it will consume{" "}
          <code className="rounded bg-slate-100 px-1.5 py-0.5">{VEHICLE_ENDPOINTS.passport}</code>{" "}
          and{" "}
          <code className="rounded bg-slate-100 px-1.5 py-0.5">{VEHICLE_ENDPOINTS.history}</code>.
        </p>
        <Link
          href="/dashboard/vehicles/sample"
          className="mt-4 inline-block rounded-md bg-amber-600 px-4 py-2 text-sm font-semibold text-white hover:bg-amber-500"
        >
          Open the passport timeline placeholder (sample route — no real vehicle)
        </Link>
      </section>
    </>
  );
}
