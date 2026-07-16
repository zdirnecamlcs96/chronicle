package chroniclekit

import (
	"reflect"
	"testing"
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
			if got := splitPath(tt.path); !reflect.DeepEqual(got, tt.segs) {
				t.Fatalf("splitPath(%q) = %#v, want %#v", tt.path, got, tt.segs)
			}
			// joinPath must produce a canonical form that re-splits identically.
			if tt.segs != nil {
				rt := splitPath(joinPath(tt.segs))
				if !reflect.DeepEqual(rt, tt.segs) {
					t.Fatalf("split(join(%#v)) = %#v", tt.segs, rt)
				}
			}
		})
	}
}

func TestPointerRoundTrip_DottedKey(t *testing.T) {
	// An object key containing "." must survive kit-path → JSON Pointer → kit-path.
	path := joinPath([]string{"a.b", "c"}) // `a\.b.c`
	ptr := toPointer(path)
	if ptr != "/a.b/c" {
		t.Fatalf("toPointer(%q) = %q, want \"/a.b/c\"", path, ptr)
	}
	if got := fromPointer(ptr); got != path {
		t.Fatalf("fromPointer(%q) = %q, want %q", ptr, got, path)
	}
}
