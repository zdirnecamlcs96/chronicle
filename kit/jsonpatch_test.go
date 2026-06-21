package chroniclekit

import (
	"testing"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
)

func TestPointerRoundTrip(t *testing.T) {
	cases := []struct{ path, pointer string }{
		{"", ""},
		{"status", "/status"},
		{"items.0.qty", "/items/0/qty"},
		{"a/b", "/a~1b"}, // "/" in a key escapes to ~1
		{"a~b", "/a~0b"}, // "~" escapes to ~0
	}
	for _, tc := range cases {
		if got := toPointer(tc.path); got != tc.pointer {
			t.Fatalf("toPointer(%q) = %q, want %q", tc.path, got, tc.pointer)
		}
		if got := fromPointer(tc.pointer); got != tc.path {
			t.Fatalf("fromPointer(%q) = %q, want %q", tc.pointer, got, tc.path)
		}
	}
}

func TestFromChanges(t *testing.T) {
	cs := []changelog.Change{
		{Path: "items.0.qty", Kind: KindPut, From: "3", To: "5"},
		{Path: "tags.2", Kind: KindCreate, To: `"new"`},
		{Path: "status", Kind: KindDelete, From: `"draft"`},
	}
	ops := FromChanges(cs)
	if len(ops) != 3 {
		t.Fatalf("want 3 ops, got %d", len(ops))
	}
	if ops[0].Op != "replace" || ops[0].Path != "/items/0/qty" || string(ops[0].Value) != "5" {
		t.Fatalf("replace mapped wrong: %+v", ops[0])
	}
	if ops[1].Op != "add" || ops[1].Path != "/tags/2" || string(ops[1].Value) != `"new"` {
		t.Fatalf("add mapped wrong: %+v", ops[1])
	}
	if ops[2].Op != "remove" || ops[2].Path != "/status" || ops[2].Value != nil {
		t.Fatalf("remove must carry no value: %+v", ops[2])
	}
}

func TestToChanges(t *testing.T) {
	ops := []Operation{
		{Op: "replace", Path: "/baz", Value: []byte(`"boo"`)},
		{Op: "add", Path: "/foo/1", Value: []byte(`"bar"`)},
		{Op: "remove", Path: "/qux"},
		{Op: "test", Path: "/baz", Value: []byte(`"boo"`)}, // skipped
		{Op: "move", Path: "/a"},                           // skipped
	}
	cs := ToChanges(ops)
	if len(cs) != 3 {
		t.Fatalf("want 3 changes (test/move skipped), got %v", cs)
	}
	if cs[0].Kind != KindPut || cs[0].Path != "baz" || cs[0].To != `"boo"` {
		t.Fatalf("replace→put wrong: %+v", cs[0])
	}
	if cs[1].Kind != KindCreate || cs[1].Path != "foo.1" {
		t.Fatalf("add→create wrong: %+v", cs[1])
	}
	if cs[2].Kind != KindDelete || cs[2].Path != "qux" || cs[2].To != "" {
		t.Fatalf("remove→delete wrong: %+v", cs[2])
	}
}
