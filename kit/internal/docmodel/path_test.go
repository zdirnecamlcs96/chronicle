package docmodel

import (
	"reflect"
	"testing"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
)

func TestSplitPath_Escapes(t *testing.T) {
	tests := []struct {
		name string
		path string
		segs []string
	}{
		{"plain", "a.b.c", []string{"a", "b", "c"}},
		{"numeric", "items.0.qty", []string{"items", "0", "qty"}},
		{"escaped dot", `a\.b`, []string{"a.b"}},
		{"escaped dot mid-path", `x.a\.b.y`, []string{"x", "a.b", "y"}},
		{"escaped backslash", `a\\b`, []string{`a\b`}},
		{"lone backslash stays literal", `a\xb`, []string{`a\xb`}},
		{"trailing backslash stays literal", `a\`, []string{`a\`}},
		{"empty", "", nil},
		{"empty segments", "a..b", []string{"a", "", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SplitPath(tt.path); !reflect.DeepEqual(got, tt.segs) {
				t.Fatalf("SplitPath(%q) = %#v, want %#v", tt.path, got, tt.segs)
			}
			// JoinPath must produce a canonical form that re-splits identically.
			if tt.segs != nil {
				rt := SplitPath(JoinPath(tt.segs))
				if !reflect.DeepEqual(rt, tt.segs) {
					t.Fatalf("SplitPath(JoinPath(%#v)) = %#v", tt.segs, rt)
				}
			}
		})
	}
}

func TestAsIndex_CapsHugeIndex(t *testing.T) {
	// A huge numeric segment must not be treated as an array index, else setIn
	// would grow an []any to that length (memory-exhaustion DoS).
	if _, ok := AsIndex("2000000000"); ok {
		t.Fatal("AsIndex accepted an index above maxIndex")
	}
	if n, ok := AsIndex("5"); !ok || n != 5 {
		t.Fatalf("AsIndex(\"5\") = %d,%v; want 5,true", n, ok)
	}
	// A poisoned change reconstructs into an object key, not a giant array.
	root, err := Apply(map[string]any{}, changelog.Change{Kind: KindPut, Path: "a.2000000000", To: "1"})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	a := root.(map[string]any)["a"]
	if _, isArray := a.([]any); isArray {
		t.Fatalf("huge index vivified an array; want object key, got %T", a)
	}
}

// TestApply_RootReplace: an empty Path is chroniclediff's only way to record a
// root-level whole-value replace (e.g. a container-type change); Apply must
// treat it as "replace root", not reject it.
func TestApply_RootReplace(t *testing.T) {
	tests := []struct {
		name string
		root any
		ch   changelog.Change
		want any
	}{
		{"array to object", []any{1, 2}, changelog.Change{Kind: KindPut, To: `{"a":1}`}, map[string]any{"a": float64(1)}},
		{"object to array", map[string]any{"a": 1}, changelog.Change{Kind: KindPut, To: `[1,2]`}, []any{float64(1), float64(2)}},
		{"scalar to scalar", float64(1), changelog.Change{Kind: KindPut, To: `2`}, float64(2)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Apply(tt.root, tt.ch)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Apply() = %#v, want %#v", got, tt.want)
			}
		})
	}

	// create/delete never target the root (Diff never emits them there); an
	// empty Path with either kind stays rejected.
	if _, err := Apply(map[string]any{}, changelog.Change{Kind: KindCreate, To: `1`}); err == nil {
		t.Fatal("want error for empty-path create")
	}
	if _, err := Apply(map[string]any{}, changelog.Change{Kind: KindDelete}); err == nil {
		t.Fatal("want error for empty-path delete")
	}
}
