"use client";

import Link from "next/link";
import { useCallback, useEffect, useRef, useState } from "react";
import { readAccessToken } from "@/lib/api/auth-token";
import { ApiError } from "@/lib/api/client";
import { listVehicles, VEHICLE_ENDPOINTS } from "@/lib/api/vehicles";
import type { Vehicle } from "@/lib/api/types";

/*
 * Vehicle registry table — wired to the listing contract (GET /v1/vehicles,
 * keyset limit+cursor pagination, contracts/vehicles/openapi.yaml 1.0.0).
 *
 * This component fetches at RUNTIME from the browser: nothing runs at build
 * time, so the production build never touches the network. Every state is
 * explicit — loading, error (with retry), empty, data (with a cursor-aware
 * "Load more" that renders only while the API returns a `next_cursor`).
 *
 * Rows deep-link into the per-vehicle passport route
 * (/dashboard/vehicles/[vehicleId]).
 */

const TABLE_HEADERS = [
  "VIN",
  "Make",
  "Model",
  "Year",
  "Latest odometer (km)",
  "Registered",
  "Passport",
] as const;

/** Contract default page size (contracts/vehicles/openapi.yaml — limit, default 50). */
const PAGE_SIZE = 50;

type LoadStatus = "loading" | "ready" | "error";

interface Failure {
  heading: string;
  detail: string;
  /** True when the API answered 401 — the sign-in wiring is still pending. */
  authRequired: boolean;
}

function describeFailure(error: unknown): Failure {
  if (error instanceof ApiError) {
    const detail =
      error.problem.detail ??
      `The API answered HTTP ${error.status} (${error.problem.code}).`;
    return {
      heading: error.message || `Request failed with status ${error.status}`,
      detail,
      authRequired: error.status === 401,
    };
  }
  return {
    heading: "Could not reach the vehicles API",
    detail: error instanceof Error ? error.message : "The network request failed.",
    authRequired: false,
  };
}

/** created_at is a UTC date-time; render the date part (deterministic, no locale drift). */
function formatDate(iso: string): string {
  return iso.slice(0, 10);
}

export function VehicleList() {
  const [vehicles, setVehicles] = useState<Vehicle[]>([]);
  const [nextCursor, setNextCursor] = useState<string | undefined>(undefined);
  const [status, setStatus] = useState<LoadStatus>("loading");
  const [failure, setFailure] = useState<Failure | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const [moreError, setMoreError] = useState<string | null>(null);
  const loadMoreController = useRef<AbortController | null>(null);

  const loadInitial = useCallback(async (signal?: AbortSignal) => {
    setStatus("loading");
    setFailure(null);
    try {
      const page = await listVehicles(
        { limit: PAGE_SIZE },
        readAccessToken(),
        signal,
      );
      if (signal?.aborted) {
        return;
      }
      setVehicles(page.vehicles);
      setNextCursor(page.next_cursor);
      setStatus("ready");
    } catch (error) {
      if (signal?.aborted) {
        return;
      }
      setFailure(describeFailure(error));
      setStatus("error");
    }
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    void loadInitial(controller.signal);
    return () => controller.abort();
  }, [loadInitial]);

  useEffect(
    () => () => {
      // Abort an in-flight "Load more" when the component unmounts.
      loadMoreController.current?.abort();
    },
    [],
  );

  const loadMore = useCallback(async () => {
    if (!nextCursor || loadingMore) {
      return;
    }
    loadMoreController.current?.abort();
    const controller = new AbortController();
    loadMoreController.current = controller;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const page = await listVehicles(
        { limit: PAGE_SIZE, cursor: nextCursor },
        readAccessToken(),
        controller.signal,
      );
      if (controller.signal.aborted) {
        return;
      }
      setVehicles((current) => [...current, ...page.vehicles]);
      setNextCursor(page.next_cursor);
    } catch (error) {
      if (controller.signal.aborted) {
        return;
      }
      // Keep the already-loaded rows; the failure renders inline next to the
      // button, which doubles as the retry control.
      setMoreError(describeFailure(error).heading);
    } finally {
      if (!controller.signal.aborted) {
        setLoadingMore(false);
      }
    }
  }, [loadingMore, nextCursor]);

  const tableBusy = loadingMore;

  return (
    <>
      <h1 className="text-2xl font-bold tracking-tight text-slate-900">Vehicles</h1>
      <p className="mt-2 max-w-3xl text-sm leading-relaxed">
        The Vehicle Registry is the canonical identity system for vehicles — one vehicle, one
        identity. This table lists the vehicles visible to your account, fetched live from{" "}
        <code className="rounded bg-slate-100 px-1.5 py-0.5">{VEHICLE_ENDPOINTS.list}</code>{" "}
        (newest first, keyset-paginated). Each row links to the vehicle&apos;s passport timeline.
      </p>

      {status === "loading" ? (
        <p
          role="status"
          aria-busy="true"
          className="mt-6 rounded-lg border border-slate-200 bg-white p-6 text-sm text-slate-600"
        >
          Loading your vehicles…
        </p>
      ) : null}

      {status === "error" && failure !== null ? (
        <section
          role="alert"
          className="mt-6 rounded-lg border border-red-200 bg-red-50 p-6"
        >
          <h2 className="text-base font-semibold text-red-900">{failure.heading}</h2>
          <p className="mt-2 text-sm leading-relaxed text-red-800">{failure.detail}</p>
          {failure.authRequired ? (
            <p className="mt-2 text-sm leading-relaxed text-red-800">
              This surface requires a Bearer access token from the identity service — sign-in
              wiring is the next integration step (see the app README).
            </p>
          ) : null}
          <button
            type="button"
            onClick={() => void loadInitial()}
            className="mt-4 rounded-md bg-red-900 px-4 py-2 text-sm font-semibold text-white hover:bg-red-800 focus-visible:outline-red-900"
          >
            Try again
          </button>
        </section>
      ) : null}

      {status === "ready" ? (
        <>
          <div className="mt-6 overflow-x-auto rounded-lg border border-slate-200 bg-white">
            <table
              className="w-full min-w-[42rem] text-left text-sm"
              aria-busy={tableBusy}
            >
              <caption className="sr-only">
                Registered vehicles — newest first, from the vehicles API
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
                {vehicles.length === 0 ? (
                  <tr>
                    <td
                      colSpan={TABLE_HEADERS.length}
                      className="px-4 py-10 text-center text-sm text-slate-600"
                    >
                      No vehicles are visible to this account yet. Register your first vehicle
                      with{" "}
                      <code className="rounded bg-slate-100 px-1.5 py-0.5">
                        {VEHICLE_ENDPOINTS.register}
                      </code>{" "}
                      and it will appear here.
                    </td>
                  </tr>
                ) : (
                  vehicles.map((vehicle) => (
                    <tr key={vehicle.id}>
                      <td className="px-4 py-3 font-mono text-xs tracking-tight text-slate-900">
                        {vehicle.vin}
                      </td>
                      <td className="px-4 py-3 text-slate-800">{vehicle.make}</td>
                      <td className="px-4 py-3 text-slate-800">{vehicle.model}</td>
                      <td className="px-4 py-3 text-slate-800">
                        {vehicle.year_of_manufacture}
                      </td>
                      <td className="px-4 py-3 text-slate-800">
                        {vehicle.mileage_latest_km.toLocaleString("en-KE")}
                      </td>
                      <td className="px-4 py-3 text-slate-800">
                        {formatDate(vehicle.created_at)}
                      </td>
                      <td className="px-4 py-3">
                        <Link
                          href={`/dashboard/vehicles/${vehicle.id}`}
                          className="font-semibold text-amber-700 underline-offset-4 hover:text-amber-800 hover:underline"
                        >
                          Open passport
                        </Link>
                      </td>
                    </tr>
                  ))
                )}
              </tbody>
            </table>
          </div>

          {nextCursor ? (
            <div className="mt-4 flex flex-wrap items-center gap-3">
              <button
                type="button"
                onClick={() => void loadMore()}
                disabled={loadingMore}
                className="rounded-md bg-slate-900 px-4 py-2 text-sm font-semibold text-white hover:bg-slate-800 disabled:cursor-not-allowed disabled:opacity-60"
              >
                {loadingMore ? "Loading more…" : "Load more"}
              </button>
              {moreError ? (
                <p role="alert" className="text-sm text-red-700">
                  {moreError} — the button retries the same page.
                </p>
              ) : null}
            </div>
          ) : null}
        </>
      ) : null}
    </>
  );
}
