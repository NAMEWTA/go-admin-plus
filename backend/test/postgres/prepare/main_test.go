package main

import "testing"

func TestRequiredSchemaNameContract(t *testing.T) {
	for _, value := range []string{"ci_01_migrations", "ci_18_demo_crud"} {
		if !schemaPattern.MatchString(value) {
			t.Fatalf("rejected required schema %q", value)
		}
	}
	for _, value := range []string{"public", "CI_01_suite", "ci_escape;drop", "ci_"} {
		if schemaPattern.MatchString(value) {
			t.Fatalf("accepted unsafe schema %q", value)
		}
	}
}
