import type { Metadata } from "next";
import { VehicleList } from "@/components/vehicles/vehicle-list";

export const metadata: Metadata = {
  title: "Vehicles",
};

/*
 * The page stays a thin server component (document title + house pattern —
 * same shape as the passport route). The list itself is the client component
 * above, which calls GET /v1/vehicles at runtime; no build-time network.
 */
export default function VehiclesPage() {
  return <VehicleList />;
}
