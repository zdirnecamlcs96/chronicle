package chronicleview

import (
	"reflect"
	"testing"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
	"github.com/zdirnecamlcs96/chronicle/kit/internal/docmodel"
	chronicleschema "github.com/zdirnecamlcs96/chronicle/kit/schema"
)

// norm JSON-normalizes v for comparison with reconstructed/snapshot values.
func norm(t *testing.T, v any) any {
	t.Helper()
	out, err := docmodel.Normalize(v)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	return out
}

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
			{Path: "name", Kind: chronicleschema.KindCreate, To: `"doc"`},
			{Path: "items.0.qty", Kind: chronicleschema.KindCreate, To: "1"},
			{Path: "items.1.qty", Kind: chronicleschema.KindCreate, To: "2"},
		}},
		{Changes: []changelog.Change{
			{Path: "items.0.qty", Kind: chronicleschema.KindPut, To: "9"},
			{Path: "name", Kind: chronicleschema.KindDelete},
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
