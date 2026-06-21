package chroniclekit

import (
	"reflect"
	"testing"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
)

func changeByPath(cs []changelog.Change, path string) (changelog.Change, bool) {
	for _, c := range cs {
		if c.Path == path {
			return c, true
		}
	}
	return changelog.Change{}, false
}

func TestDiff_NoChange(t *testing.T) {
	doc := map[string]any{"a": 1, "b": map[string]any{"c": 2}}
	cs, err := Diff(doc, doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 0 {
		t.Fatalf("identical states must diff to nothing, got %v", cs)
	}
}

func TestDiff_LeafPut(t *testing.T) {
	cs, _ := Diff(map[string]any{"a": 1}, map[string]any{"a": 2})
	if len(cs) != 1 {
		t.Fatalf("want 1 change, got %v", cs)
	}
	c := cs[0]
	if c.Path != "a" || c.Kind != KindPut || c.From != "1" || c.To != "2" {
		t.Fatalf("bad put: %+v", c)
	}
}

func TestDiff_CreateAndDelete(t *testing.T) {
	create, _ := Diff(map[string]any{"a": 1}, map[string]any{"a": 1, "b": 2})
	if c, ok := changeByPath(create, "b"); !ok || c.Kind != KindCreate || c.To != "2" || c.From != "" {
		t.Fatalf("want create b To=2 From empty, got %v", create)
	}
	del, _ := Diff(map[string]any{"a": 1, "b": 2}, map[string]any{"a": 1})
	if c, ok := changeByPath(del, "b"); !ok || c.Kind != KindDelete || c.From != "2" || c.To != "" {
		t.Fatalf("want delete b From=2 To empty, got %v", del)
	}
}

func TestDiff_Nested(t *testing.T) {
	cs, _ := Diff(
		map[string]any{"x": map[string]any{"y": 1, "z": 9}},
		map[string]any{"x": map[string]any{"y": 2, "z": 9}},
	)
	if len(cs) != 1 {
		t.Fatalf("want 1 nested change, got %v", cs)
	}
	if cs[0].Path != "x.y" {
		t.Fatalf("want path x.y, got %q", cs[0].Path)
	}
}

func TestDiff_Array(t *testing.T) {
	// element change
	cs, _ := Diff(map[string]any{"a": []any{1, 2}}, map[string]any{"a": []any{1, 3}})
	if c, ok := changeByPath(cs, "a.1"); !ok || c.Kind != KindPut || c.To != "3" {
		t.Fatalf("want put a.1 To=3, got %v", cs)
	}
	// grow
	grow, _ := Diff(map[string]any{"a": []any{1}}, map[string]any{"a": []any{1, 2}})
	if c, ok := changeByPath(grow, "a.1"); !ok || c.Kind != KindCreate || c.To != "2" {
		t.Fatalf("want create a.1, got %v", grow)
	}
	// shrink
	shrink, _ := Diff(map[string]any{"a": []any{1, 2}}, map[string]any{"a": []any{1}})
	if c, ok := changeByPath(shrink, "a.1"); !ok || c.Kind != KindDelete {
		t.Fatalf("want delete a.1, got %v", shrink)
	}
}

// TestDiff_Reconstruct_RoundTrip is the end-to-end guarantee: build `a` from nil,
// then diff a→b; replaying both commits must yield exactly `b`.
func TestDiff_Reconstruct_RoundTrip(t *testing.T) {
	cases := []struct{ a, b any }{
		{map[string]any{"name": "x"}, map[string]any{"name": "y"}},
		{
			map[string]any{"items": []any{map[string]any{"qty": 1, "sku": "A"}}},
			map[string]any{"items": []any{map[string]any{"qty": 5, "sku": "A"}}},
		},
		{
			map[string]any{"a": 1, "obj": map[string]any{"k": "v"}},
			map[string]any{"a": 1, "obj": map[string]any{"k": "v", "new": true}, "added": 2},
		},
		{
			map[string]any{"a": 1, "gone": map[string]any{"k": "v"}},
			map[string]any{"a": 2},
		},
	}
	for i, tc := range cases {
		build, _ := Diff(nil, tc.a)
		change, _ := Diff(tc.a, tc.b)
		got, err := Reconstruct([]changelog.Commit{{Changes: build}, {Changes: change}})
		if err != nil {
			t.Fatalf("case %d reconstruct: %v", i, err)
		}
		if !reflect.DeepEqual(got, norm(t, tc.b)) {
			t.Fatalf("case %d round-trip mismatch:\n got  %#v\n want %#v", i, got, norm(t, tc.b))
		}
	}
}
