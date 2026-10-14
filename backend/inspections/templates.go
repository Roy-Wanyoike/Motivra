package inspections

import (
	"fmt"

	"github.com/google/uuid"
)

// The template registry. System templates are owned HERE, in code — they
// are product content (what an inspection must cover per vehicle class),
// not demo data, and never live only in a database where they could drift
// from the code that validates findings against them. Sessions and reports
// snapshot the resolved template (id, name, version) so later registry
// changes never reinterpret a historical inspection.
//
// templateNamespace anchors the deterministic template identifiers: every
// template ID is a uuid v5 of "<name>@v<version>" under this namespace, so
// the same template resolves to the same identifier in every process and
// release.
var templateNamespace = uuid.MustParse("8c2f4b6a-9d1e-4a57-b3c8-5d6e7f809a1b")

// registry is the process-wide system template set, built once at init.
var registry = buildRegistry()

// TemplateNotFoundError is returned when a template id does not resolve in
// the registry. The HTTP layer maps it to a platform 404.
type TemplateNotFoundError struct {
	ID uuid.UUID
}

func (e *TemplateNotFoundError) Error() string {
	return fmt.Sprintf("inspections: template %s is not registered", e.ID)
}

// NoActiveTemplateError is returned when a vehicle class has no active
// template (a registry integrity violation; every known class must have
// one — pinned by the registry tests).
type NoActiveTemplateError struct {
	Class VehicleClass
}

func (e *NoActiveTemplateError) Error() string {
	return fmt.Sprintf("inspections: no active template for vehicle class %s", e.Class)
}

// buildRegistry assembles the system templates, one per vehicle class,
// assigning deterministic identifiers.
func buildRegistry() []Template {
	templates := []Template{
		{
			VehicleClass: ClassMotorcycle,
			Name:         "Motivra Standard Motorcycle Inspection",
			Version:      1,
			Items: []TemplateItem{
				{Key: "brakes_front", Label: "Front brake pads and disc condition", Required: true},
				{Key: "brakes_rear", Label: "Rear brake and drum condition", Required: true},
				{Key: "tyres", Label: "Tyre tread, pressure and sidewall condition", Required: true},
				{Key: "chain_sprockets", Label: "Drive chain tension and sprocket wear", Required: true},
				{Key: "lights_electrical", Label: "Headlamp, brake light, indicators and horn", Required: true},
				{Key: "frame_suspension", Label: "Frame, forks and rear suspension", Required: true},
				{Key: "exhaust_emissions", Label: "Exhaust condition and visible smoke", Required: false},
				{Key: "odometer_consistency", Label: "Odometer reading consistent with wear", Required: true},
			},
		},
		{
			VehicleClass: ClassSedan,
			Name:         "Motivra Standard Sedan Inspection",
			Version:      1,
			Items: []TemplateItem{
				{Key: "brakes", Label: "Brake pads, discs and fluid condition", Required: true},
				{Key: "tyres", Label: "Tyre tread depth, pressure and spare", Required: true},
				{Key: "suspension_steering", Label: "Suspension bushings and steering play", Required: true},
				{Key: "lights_electrical", Label: "Exterior lights, horn and battery", Required: true},
				{Key: "engine_leaks", Label: "Engine bay: leaks, belts and fluids", Required: true},
				{Key: "transmission", Label: "Gearbox and clutch / automatic behaviour", Required: true},
				{Key: "body_panel_condition", Label: "Body panels, doors and glass", Required: false},
				{Key: "odometer_consistency", Label: "Odometer reading consistent with wear", Required: true},
			},
		},
		{
			VehicleClass: ClassSUV,
			Name:         "Motivra Standard SUV Inspection",
			Version:      1,
			Items: []TemplateItem{
				{Key: "brakes", Label: "Brake pads, discs and fluid condition", Required: true},
				{Key: "tyres", Label: "Tyre tread depth, pressure and spare", Required: true},
				{Key: "suspension_steering", Label: "Suspension, steering play and 4x4 engagement", Required: true},
				{Key: "lights_electrical", Label: "Exterior lights, horn and battery", Required: true},
				{Key: "engine_leaks", Label: "Engine bay: leaks, belts and fluids", Required: true},
				{Key: "transmission", Label: "Gearbox and transfer case behaviour", Required: true},
				{Key: "chassis_underbody", Label: "Underbody, chassis rails and mounting points", Required: true},
				{Key: "odometer_consistency", Label: "Odometer reading consistent with wear", Required: true},
			},
		},
		{
			VehicleClass: ClassPickup,
			Name:         "Motivra Standard Pickup Inspection",
			Version:      1,
			Items: []TemplateItem{
				{Key: "brakes", Label: "Brake pads, discs and load-sensing valve", Required: true},
				{Key: "tyres", Label: "Tyre tread depth, pressure and spare", Required: true},
				{Key: "suspension_steering", Label: "Suspension, leaf springs and steering play", Required: true},
				{Key: "lights_electrical", Label: "Exterior lights, horn and battery", Required: true},
				{Key: "engine_leaks", Label: "Engine bay: leaks, belts and fluids", Required: true},
				{Key: "transmission", Label: "Gearbox and differential behaviour", Required: true},
				{Key: "load_bed_tailgate", Label: "Load bed, tailgate and towing gear", Required: false},
				{Key: "odometer_consistency", Label: "Odometer reading consistent with wear", Required: true},
			},
		},
		{
			VehicleClass: ClassTruck,
			Name:         "Motivra Standard Truck Inspection",
			Version:      1,
			Items: []TemplateItem{
				{Key: "brakes_air", Label: "Air brake system, tanks and governor", Required: true},
				{Key: "tyres", Label: "Tyre tread depth, pressure and spare", Required: true},
				{Key: "suspension_steering", Label: "Suspension, axles and steering play", Required: true},
				{Key: "lights_electrical", Label: "Exterior lights, horn and battery isolation", Required: true},
				{Key: "engine_leaks", Label: "Engine bay: leaks, belts and fluids", Required: true},
				{Key: "transmission", Label: "Gearbox, retarder and PTO behaviour", Required: true},
				{Key: "chassis_underbody", Label: "Chassis, fifth wheel / body mounting", Required: true},
				{Key: "cargo_restraint", Label: "Cargo restraint and load security", Required: false},
				{Key: "odometer_consistency", Label: "Odometer / tachograph consistency", Required: true},
			},
		},
		{
			VehicleClass: ClassBus,
			Name:         "Motivra Standard Bus Inspection",
			Version:      1,
			Items: []TemplateItem{
				{Key: "brakes_air", Label: "Air brake system, tanks and governor", Required: true},
				{Key: "tyres", Label: "Tyre tread depth, pressure and spare", Required: true},
				{Key: "suspension_steering", Label: "Suspension, axles and steering play", Required: true},
				{Key: "lights_electrical", Label: "Exterior lights, horn and battery isolation", Required: true},
				{Key: "engine_leaks", Label: "Engine bay: leaks, belts and fluids", Required: true},
				{Key: "transmission", Label: "Gearbox and retarder behaviour", Required: true},
				{Key: "passenger_safety", Label: "Emergency exits, handrails and seating", Required: true},
				{Key: "odometer_consistency", Label: "Odometer reading consistent with wear", Required: true},
			},
		},
	}
	for i := range templates {
		templates[i].Active = true
		templates[i].ID = TemplateID(templates[i].Name, templates[i].Version)
	}
	return templates
}

// TemplateID derives the deterministic identifier of a template version.
func TemplateID(name string, version int) uuid.UUID {
	return uuid.NewSHA1(templateNamespace, []byte(fmt.Sprintf("%s@v%d", name, version)))
}

// SystemTemplates returns the registered system templates in class order.
// The returned slice is a copy; callers cannot mutate the registry (the
// item slices are shared, but the registry treats them as immutable).
func SystemTemplates() []Template {
	out := make([]Template, len(registry))
	copy(out, registry)
	return out
}

// TemplateByID returns the registered template with the given id, or an
// error wrapping *TemplateNotFoundError.
func TemplateByID(id uuid.UUID) (Template, error) {
	for _, t := range registry {
		if t.ID == id {
			return t, nil
		}
	}
	return Template{}, &TemplateNotFoundError{ID: id}
}

// ActiveTemplateForClass returns the active template for the vehicle
// class, or an error wrapping *NoActiveTemplateError.
func ActiveTemplateForClass(class VehicleClass) (Template, error) {
	for _, t := range registry {
		if t.VehicleClass == class && t.Active {
			return t, nil
		}
	}
	return Template{}, &NoActiveTemplateError{Class: class}
}
