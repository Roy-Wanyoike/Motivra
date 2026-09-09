import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { VehicleList } from "@/components/vehicles/vehicle-list";
import type { Vehicle } from "@/lib/api/types";

/*
 * The fetch layer is mocked (never the component or the client functions):
 * each test queues HTTP-shaped responses and asserts on what the vehicle
 * list renders from them — mirroring the wire format of
 * contracts/vehicles/openapi.yaml (GET /v1/vehicles, VehicleList).
 */

const VEHICLE_A: Vehicle = {
  id: "11111111-1111-4111-8111-111111111111",
  tenant_id: null,
  owner_user_id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  vin: "1HGCM82633A004352",
  make: "Honda",
  model: "Accord",
  year_of_manufacture: 2019,
  mileage_latest_km: 152300,
  created_at: "2026-09-01T10:00:00Z",
  updated_at: "2026-09-01T10:00:00Z",
};

const VEHICLE_B: Vehicle = {
  id: "22222222-2222-4222-8222-222222222222",
  tenant_id: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
  owner_user_id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  vin: "WBA3B1C50DF461234",
  make: "BMW",
  model: "3 Series",
  year_of_manufacture: 2021,
  plate: "KDA 001A",
  color: "Midnight Blue",
  mileage_latest_km: 84210,
  created_at: "2026-09-08T08:30:00Z",
  updated_at: "2026-09-08T08:30:00Z",
};

type QueuedResponse = { status: number; body: unknown };

/** Minimal fetch-shaped stub — only what lib/api/client.ts consumes. */
function stubFetch(responses: QueuedResponse[]): ReturnType<typeof vi.fn> {
  const fetchMock = vi.fn();
  for (const { status, body } of responses) {
    fetchMock.mockImplementationOnce(() =>
      Promise.resolve({
        ok: status >= 200 && status < 300,
        status,
        headers: {
          get: (name: string) =>
            name.toLowerCase() === "content-type" ? "application/json" : null,
        },
        json: () => Promise.resolve(body),
      }),
    );
  }
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Vehicle list (GET /v1/vehicles integration)", () => {
  it("renders rows from the listing response and deep-links each passport", async () => {
    const fetchMock = stubFetch([
      { status: 200, body: { vehicles: [VEHICLE_A, VEHICLE_B] } },
    ]);

    render(<VehicleList />);

    // Loading state is visible before the response resolves.
    expect(screen.getByRole("status")).toHaveTextContent(/loading your vehicles/i);

    await screen.findByText("1HGCM82633A004352");
    expect(screen.getByText("WBA3B1C50DF461234")).toBeInTheDocument();
    expect(screen.getByText("Honda")).toBeInTheDocument();
    expect(screen.getByText("BMW")).toBeInTheDocument();

    // Passport deep-links reuse the vehicle id from the response.
    const passportLinks = screen.getAllByRole("link", { name: /open passport/i });
    expect(passportLinks).toHaveLength(2);
    expect(passportLinks.map((link) => link.getAttribute("href"))).toEqual([
      `/dashboard/vehicles/${VEHICLE_A.id}`,
      `/dashboard/vehicles/${VEHICLE_B.id}`,
    ]);

    // First page request: contract path + default page size, no cursor.
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/v1/vehicles?limit=50");
    expect((init as RequestInit).method).toBe("GET");
    // No token is stored in the test environment; the client must not invent one.
    expect((init as RequestInit).headers).not.toHaveProperty("Authorization");

    // The page is the last one (no next_cursor) → no "Load more".
    expect(screen.queryByRole("button", { name: /load more/i })).not.toBeInTheDocument();
  });

  it("shows the honest empty state when the caller has zero vehicles", async () => {
    stubFetch([{ status: 200, body: { vehicles: [] } }]);

    render(<VehicleList />);

    expect(
      await screen.findByText(/no vehicles are visible to this account yet/i),
    ).toBeInTheDocument();
    // The empty state names the real registration endpoint, not a placeholder.
    expect(screen.getByText(/POST \/v1\/vehicles/)).toBeInTheDocument();
    expect(screen.queryByText("1HGCM82633A004352")).not.toBeInTheDocument();
  });

  it("renders the error state with a working retry after a failed request", async () => {
    const fetchMock = stubFetch([
      {
        status: 500,
        body: { code: "internal_error", title: "Internal error", detail: "The registry is unreachable." },
      },
      // Retry hits the same endpoint and succeeds.
      { status: 200, body: { vehicles: [VEHICLE_A] } },
    ]);

    render(<VehicleList />);

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(/internal error/i);
    expect(alert).toHaveTextContent(/the registry is unreachable/i);
    expect(screen.queryByText("1HGCM82633A004352")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /try again/i }));

    expect(await screen.findByText("1HGCM82633A004352")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(fetchMock.mock.calls[1][0]).toBe("/v1/vehicles?limit=50");
  });

  it("surfaces the API's 401 as an authentication hint in the error state", async () => {
    stubFetch([
      {
        status: 401,
        body: { code: "unauthorized", title: "Authentication required", detail: "Bearer token missing." },
      },
    ]);

    render(<VehicleList />);

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(/authentication required/i);
    // Honest wiring note — no fake data, no fake success.
    expect(alert).toHaveTextContent(/bearer access token/i);
    expect(screen.getByRole("button", { name: /try again/i })).toBeInTheDocument();
  });

  it("Load more appends the next page by passing next_cursor back as cursor", async () => {
    const fetchMock = stubFetch([
      { status: 200, body: { vehicles: [VEHICLE_A], next_cursor: "cursor-token-1" } },
      // Second (last) page: no next_cursor afterwards.
      { status: 200, body: { vehicles: [VEHICLE_B] } },
    ]);

    render(<VehicleList />);

    expect(await screen.findByText("1HGCM82633A004352")).toBeInTheDocument();
    expect(screen.queryByText("WBA3B1C50DF461234")).not.toBeInTheDocument();

    const loadMore = screen.getByRole("button", { name: /load more/i });
    fireEvent.click(loadMore);

    // The appended page arrives and the cursor is exhausted → button leaves the DOM.
    expect(await screen.findByText("WBA3B1C50DF461234")).toBeInTheDocument();
    await waitFor(() => {
      expect(screen.queryByRole("button", { name: /load more/i })).not.toBeInTheDocument();
    });

    // Row A (page 1) is still present — pages append, never replace.
    expect(screen.getByText("1HGCM82633A004352")).toBeInTheDocument();

    // Second request carries the opaque cursor verbatim.
    expect(fetchMock.mock.calls[1][0]).toBe("/v1/vehicles?limit=50&cursor=cursor-token-1");
  });
});
