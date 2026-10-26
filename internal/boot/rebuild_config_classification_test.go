package boot

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"reasonix/internal/config"
)

// Classes describe ownership, not a promise that each raw field is hashed:
// runtime includes derived dependencies, live is updated without replacement,
// and excluded covers presentation, other host lifetimes and retired settings.
// Container classes never exempt their nested exported fields from review.
type configFieldClasses struct {
	runtime, live, excluded string
}

type configTypeClassification struct {
	typeOf reflect.Type
	fields configFieldClasses
}

func classifyConfigType[T any](fields configFieldClasses) configTypeClassification {
	return configTypeClassification{reflect.TypeFor[T](), fields}
}

func runtimeConfigClassification() []configTypeClassification {
	return append(runtimeConfigCoreClassification(), runtimeConfigHostClassification()...)
}

func TestRuntimeConfigClassificationCoversExportedFields(t *testing.T) {
	for _, issue := range configClassificationIssues(reflect.TypeFor[config.Config](), runtimeConfigClassification()) {
		t.Error(issue)
	}
}

func configClassificationIssues(root reflect.Type, schema []configTypeClassification) []string {
	index := make(map[reflect.Type]configFieldClasses, len(schema))
	var issues []string
	for _, entry := range schema {
		if _, duplicate := index[entry.typeOf]; duplicate {
			issues = append(issues, fmt.Sprintf("duplicate type classification: %v", entry.typeOf))
		}
		index[entry.typeOf] = entry.fields
	}
	visited := map[reflect.Type]bool{}
	var visit func(reflect.Type, string)
	visit = func(typ reflect.Type, path string) {
		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			visit(typ.Elem(), path+"[]")
		case reflect.Map:
			visit(typ.Key(), path+"{key}")
			visit(typ.Elem(), path+"{value}")
		case reflect.Struct:
			if visited[typ] {
				return
			}
			visited[typ] = true
			classes, known := index[typ]
			if !known {
				issues = append(issues, "unclassified type at "+path)
			}
			issues = append(issues, classifiedFieldIssues(typ, path, classes)...)
			for field := range typ.NumField() {
				f := typ.Field(field)
				if f.IsExported() {
					visit(f.Type, path+"."+f.Name)
				}
			}
		}
	}
	visit(root, "Config")
	for typ := range index {
		if !visited[typ] {
			issues = append(issues, fmt.Sprintf("stale type classification: %v", typ))
		}
	}
	slices.Sort(issues)
	return issues
}

func classifiedFieldIssues(typ reflect.Type, path string, classes configFieldClasses) []string {
	classified := map[string]bool{}
	var issues []string
	for _, group := range []string{classes.runtime, classes.live, classes.excluded} {
		for name := range strings.FieldsSeq(group) {
			if classified[name] {
				issues = append(issues, "duplicate field classification: "+path+"."+name)
			}
			classified[name] = true
		}
	}
	remaining := maps.Clone(classified)
	for field := range typ.NumField() {
		f := typ.Field(field)
		if !f.IsExported() {
			continue
		}
		if !classified[f.Name] {
			issues = append(issues, "unclassified field: "+path+"."+f.Name)
		}
		delete(remaining, f.Name)
	}
	for name := range remaining {
		issues = append(issues, "stale field classification: "+path+"."+name)
	}
	return issues
}

func TestRuntimeConfigClassificationRejectsAddedFields(t *testing.T) {
	cases := []struct {
		name, parent string
		target       reflect.Type
	}{
		{"top level", "", reflect.TypeFor[config.Config]()},
		{"nested runtime", "Agent", reflect.TypeFor[config.AgentConfig]()},
		{"nested excluded", "UI", reflect.TypeFor[config.UIConfig]()},
		{"upload admission", "Attachments", reflect.TypeFor[config.AttachmentsConfig]()},
		{"slice element", "Providers", reflect.TypeFor[config.ProviderEntry]()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := configClassificationStructFields(tc.target)
			mutant := reflect.StructOf(append(fields, reflect.StructField{Name: "FutureSetting", Type: reflect.TypeFor[string]()}))
			schema := replaceClassifiedType(runtimeConfigClassification(), tc.target, mutant)
			root := mutant
			if tc.parent != "" {
				rootFields := configClassificationStructFields(reflect.TypeFor[config.Config]())
				for i := range rootFields {
					if rootFields[i].Name == tc.parent {
						rootFields[i].Type = mutant
						if tc.parent == "Providers" {
							rootFields[i].Type = reflect.SliceOf(mutant)
						}
					}
				}
				root = reflect.StructOf(rootFields)
				schema = replaceClassifiedType(schema, reflect.TypeFor[config.Config](), root)
			}
			path := "Config.FutureSetting"
			if tc.parent != "" {
				parent := tc.parent
				if parent == "Providers" {
					parent += "[]"
				}
				path = "Config." + parent + ".FutureSetting"
			}
			assertConfigClassificationIssue(t, root, schema, "unclassified field: ", path)
		})
	}
}

func configClassificationStructFields(typ reflect.Type) []reflect.StructField {
	fields := make([]reflect.StructField, typ.NumField())
	for i := range fields {
		fields[i] = typ.Field(i)
	}
	return fields
}

func replaceClassifiedType(schema []configTypeClassification, old, next reflect.Type) []configTypeClassification {
	for i := range schema {
		if schema[i].typeOf == old {
			schema[i].typeOf = next
		}
	}
	return schema
}

func assertConfigClassificationIssue(t *testing.T, root reflect.Type, schema []configTypeClassification, prefix, field string) {
	t.Helper()
	for _, issue := range configClassificationIssues(root, schema) {
		if strings.Contains(issue, prefix) && strings.HasSuffix(issue, field) {
			return
		}
	}
	t.Fatalf("classification guard missed %q ending in %q", prefix, field)
}
