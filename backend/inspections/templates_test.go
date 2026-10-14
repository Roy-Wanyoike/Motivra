package inspections

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRegistryCoversEveryVehicleClass pins the registry integrity
// invariant: every known vehicle class has exactly one active template, so
// CreateSession can never fail with NoActiveTemplateError for a valid
// class.
func TestRegistryCoversEveryVehicleClass(t *testing.T) {
	t.Parallel()
	templates := SystemTemplates()
	require.Len(t, templates, len(AllVehicleClasses()))

	byClass := map[VehicleClass]int{}
	for _, tpl := range templates {
		byClass[tpl.VehicleClass]++
		require.True(t, tpl.Active, "system template %s must be active", tpl.Name)
		require.NotEmpty(t, tpl.Items, "template %s must carry items", tpl.Name)
		require.Positive(t, tpl.Version)
	}
	for _, class := range AllVehicleClasses() {
		assert.Equal(t, 1, byClass[class], "exactly one template for class %s", class)
		template, err := ActiveTemplateForClass(class)
		require.NoError(t, err)
		assert.Equal(t, class, template.VehicleClass)
	}
}

// TestTemplateIDsAreDeterministic pins that the same template version
// always resolves to the same identifier (uuid v5 of name@version), and
// that distinct templates never collide.
func TestTemplateIDsAreDeterministic(t *testing.T) {
	t.Parallel()
	seen := map[string]string{}
	for _, tpl := range SystemTemplates() {
		recomputed := TemplateID(tpl.Name, tpl.Version)
		assert.Equal(t, recomputed, tpl.ID, "template %s id must be derived from name@version", tpl.Name)
		assert.NotContains(t, seen, tpl.ID.String(), "template ids must be unique")
		seen[tpl.ID.String()] = tpl.Name
	}
	assert.Equal(t, TemplateID("Motivra Standard Sedan Inspection", 1), TemplateID("Motivra Standard Sedan Inspection", 1))
	assert.NotEqual(t, TemplateID("Motivra Standard Sedan Inspection", 1), TemplateID("Motivra Standard Sedan Inspection", 2))
}

// TestTemplateLookupByUnknownID maps an unknown id to *TemplateNotFoundError.
func TestTemplateLookupByUnknownID(t *testing.T) {
	t.Parallel()
	_, err := TemplateByID(TemplateID("Never Registered", 9))
	require.Error(t, err)
	var notFound *TemplateNotFoundError
	assert.ErrorAs(t, err, &notFound)
}

// TestTemplateHelpersExerciseItemModel covers Item, RequiredItems and the
// canonical-form safety of the registry content (names must not carry
// separators that could break the report hash lines).
func TestTemplateHelpersExerciseItemModel(t *testing.T) {
	t.Parallel()
	sedan, err := ActiveTemplateForClass(ClassSedan)
	require.NoError(t, err)

	item, ok := sedan.Item("brakes")
	require.True(t, ok)
	assert.Equal(t, "brakes", item.Key)
	_, ok = sedan.Item("does_not_exist")
	assert.False(t, ok)

	required := sedan.RequiredItems()
	assert.NotEmpty(t, required)
	for _, key := range required {
		it, ok := sedan.Item(key)
		require.True(t, ok)
		assert.True(t, it.Required, "RequiredItems returned %q", key)
	}
	assert.NotContains(t, required, "body_panel_condition", "the optional item is not required")

	for _, tpl := range SystemTemplates() {
		assert.NotContains(t, tpl.Name, "\n", "template names must not break the canonical hash lines")
		assert.NotContains(t, tpl.Name, ",", "template names must not carry canonical separators")
		assert.False(t, strings.Contains(tpl.Name, "  "), "no accidental double spaces")
	}
}

// TestUnknownClassHasNoTemplate pins the NoActiveTemplateError surface.
func TestUnknownClassHasNoTemplate(t *testing.T) {
	t.Parallel()
	_, err := ActiveTemplateForClass("SPACESHIP")
	require.Error(t, err)
	var noTemplate *NoActiveTemplateError
	assert.ErrorAs(t, err, &noTemplate)
}
