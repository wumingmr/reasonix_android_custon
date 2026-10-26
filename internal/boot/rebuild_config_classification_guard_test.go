package boot

import (
	"reflect"
	"testing"

	"reasonix/internal/config"
)

func TestConfigClassificationRejectsInvalidLedger(t *testing.T) {
	type fixture struct {
		Known  string
		hidden string //nolint:unused // Reflection fixture verifies that private fields cannot be classified.
	}
	root := reflect.TypeFor[fixture]()
	base := classifyConfigType[fixture](configFieldClasses{runtime: "Known"})
	if issues := configClassificationIssues(root, []configTypeClassification{base}); len(issues) != 0 {
		t.Fatalf("valid ledger rejected: %v", issues)
	}
	cases := []struct {
		name, issue, field string
		schema             []configTypeClassification
	}{
		{"unclassified", "unclassified field:", "Known", []configTypeClassification{{root, configFieldClasses{}}}},
		{"same class duplicate", "duplicate field classification:", "Known", []configTypeClassification{
			{root, configFieldClasses{runtime: "Known Known"}},
		}},
		{"cross class duplicate", "duplicate field classification:", "Known", []configTypeClassification{
			{root, configFieldClasses{runtime: "Known", live: "Known"}},
		}},
		{"removed field", "stale field classification:", "Removed", []configTypeClassification{
			{root, configFieldClasses{runtime: "Known Removed"}},
		}},
		{"unexported field", "stale field classification:", "hidden", []configTypeClassification{
			{root, configFieldClasses{runtime: "Known hidden"}},
		}},
		{"duplicate type", "duplicate type classification:", "fixture", []configTypeClassification{base, base}},
		{"unclassified type", "unclassified type at", "Config", nil},
		{"stale type", "stale type classification:", "UIConfig", []configTypeClassification{
			base, classifyConfigType[config.UIConfig](configFieldClasses{}),
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertConfigClassificationIssue(t, root, tc.schema, tc.issue, tc.field)
		})
	}
}

func TestConfigClassificationInspectsEveryContainer(t *testing.T) {
	child := reflect.TypeFor[struct{ FutureSetting string }]()
	cases := []struct {
		name string
		typ  reflect.Type
	}{
		{"direct", child},
		{"pointer", reflect.PointerTo(child)},
		{"slice", reflect.SliceOf(child)},
		{"array", reflect.ArrayOf(1, child)},
		{"map value", reflect.MapOf(reflect.TypeFor[string](), child)},
		{"map key", reflect.MapOf(child, reflect.TypeFor[string]())},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := reflect.StructOf([]reflect.StructField{{Name: "Child", Type: tc.typ}})
			schema := []configTypeClassification{
				{root, configFieldClasses{excluded: "Child"}},
				{child, configFieldClasses{}},
			}
			assertConfigClassificationIssue(t, root, schema, "unclassified field:", "FutureSetting")
			assertConfigClassificationIssue(t, root, schema[:1], "unclassified type at", "Child"+containerSuffix(tc.typ))
		})
	}
}

func containerSuffix(typ reflect.Type) string {
	switch typ.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return "[]"
	case reflect.Map:
		if typ.Key().Kind() == reflect.Struct {
			return "{key}"
		}
		return "{value}"
	default:
		return ""
	}
}

func TestConfigClassificationHandlesRecursiveTypes(t *testing.T) {
	type recursive struct {
		Next *recursive
	}
	schema := []configTypeClassification{classifyConfigType[recursive](configFieldClasses{runtime: "Next"})}
	if issues := configClassificationIssues(reflect.TypeFor[recursive](), schema); len(issues) != 0 {
		t.Fatalf("recursive classified type rejected: %v", issues)
	}
}
