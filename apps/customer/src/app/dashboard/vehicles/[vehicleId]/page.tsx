import { VehiclePassportTimeline } from "@/components/vehicles/vehicle-passport-timeline";

export const metadata = {
  title: "Vehicle Passport",
};

export default async function VehiclePassportPage({
  params,
}: {
  params: Promise<{ vehicleId: string }>;
}) {
  const { vehicleId } = await params;
  return <VehiclePassportTimeline vehicleId={vehicleId} />;
}
