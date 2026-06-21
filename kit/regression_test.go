package chroniclekit

import (
	"reflect"
	"testing"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
)

// roundTrip builds `a` from nil, diffs a→b, and asserts replaying both commits
// yields exactly `b` — the core Diff/Reconstruct guarantee.
func roundTrip(t *testing.T, a, b any) {
	t.Helper()
	build, _ := Diff(nil, a)
	change, _ := Diff(a, b)
	got, err := Reconstruct([]changelog.Commit{{Changes: build}, {Changes: change}})
	if err != nil {
		t.Fatalf("reconstruct: %v", err)
	}
	if !reflect.DeepEqual(got, norm(t, b)) {
		t.Fatalf("round-trip mismatch:\n got  %#v\n want %#v", got, norm(t, b))
	}
}

// Regressions for bugs found in adversarial review of the kit.
func TestRoundTrip_Regressions(t *testing.T) {
	cases := []struct {
		name string
		a, b any
	}{
		// F01: multi-element array shrink (ascending deletes used to corrupt).
		{"array multi-shrink", map[string]any{"a": []any{1, 2, 3}}, map[string]any{"a": []any{1}}},
		// F02/F04: present JSON null must be preserved, not collapsed to {}/[].
		{"object to null", map[string]any{"obj": map[string]any{"k": "v"}}, map[string]any{"obj": nil}},
		{"array to null", map[string]any{"a": []any{1, 2}}, map[string]any{"a": nil}},
		{"leaf to null", map[string]any{"x": 1}, map[string]any{"x": nil}},
		{"null to object", map[string]any{"obj": nil}, map[string]any{"obj": map[string]any{"k": "v"}}},
		// F05: numeric object keys at an existing container stay keys (not indices).
		{"numeric object key", map[string]any{"0": "x", "1": "y"}, map[string]any{"0": "z", "1": "y"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { roundTrip(t, tc.a, tc.b) })
	}
}
