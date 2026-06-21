package chroniclekit

import (
	"reflect"
	"testing"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
)

func TestLCAPath(t *testing.T) {
	cases := []struct {
		paths []string
		want  string
	}{
		{[]string{"items.0.qty", "items.0.price"}, "items.0"}, // clustered → tight parent
		{[]string{"items.0.qty", "items.2.price"}, "items"},   // different elements → items
		{[]string{"status", "items.0.qty"}, ""},               // scattered → root
		{[]string{"items.0.qty"}, "items.0.qty"},              // single → full path (caller climbs)
		{[]string{"a.b", "a.b"}, "a.b"},                       // identical
		{nil, ""},
	}
	for _, tc := range cases {
		if got := lcaPath(tc.paths); got != tc.want {
			t.Fatalf("lcaPath(%v) = %q, want %q", tc.paths, got, tc.want)
		}
	}
}

func TestReconstruct_NestedAndArray(t *testing.T) {
	commits := []changelog.Commit{
		{Changes: []changelog.Change{
			{Path: "name", Kind: KindCreate, To: `"doc"`},
			{Path: "items.0.qty", Kind: KindCreate, To: "1"},
			{Path: "items.1.qty", Kind: KindCreate, To: "2"},
		}},
		{Changes: []changelog.Change{
			{Path: "items.0.qty", Kind: KindPut, To: "9"},
			{Path: "name", Kind: KindDelete},
		}},
	}
	got, err := Reconstruct(commits)
	if err != nil {
		t.Fatal(err)
	}
	want := norm(t, map[string]any{
		"items": []any{map[string]any{"qty": 9}, map[string]any{"qty": 2}},
	})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reconstruct mismatch:\n got  %#v\n want %#v", got, want)
	}
}
