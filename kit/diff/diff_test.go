package chroniclediff

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
	"github.com/zdirnecamlcs96/chronicle/kit/internal/docmodel"
	chronicleschema "github.com/zdirnecamlcs96/chronicle/kit/schema"
	chronicleview "github.com/zdirnecamlcs96/chronicle/kit/view"
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
	if c.Path != "a" || c.Kind != chronicleschema.KindPut || c.From != "1" || c.To != "2" {
		t.Fatalf("bad put: %+v", c)
	}
}

func TestDiff_CreateAndDelete(t *testing.T) {
	create, _ := Diff(map[string]any{"a": 1}, map[string]any{"a": 1, "b": 2})
	if c, ok := changeByPath(create, "b"); !ok || c.Kind != chronicleschema.KindCreate || c.To != "2" || c.From != "" {
		t.Fatalf("want create b To=2 From empty, got %v", create)
	}
	del, _ := Diff(map[string]any{"a": 1, "b": 2}, map[string]any{"a": 1})
	if c, ok := changeByPath(del, "b"); !ok || c.Kind != chronicleschema.KindDelete || c.From != "2" || c.To != "" {
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
	if c, ok := changeByPath(cs, "a.1"); !ok || c.Kind != chronicleschema.KindPut || c.To != "3" {
		t.Fatalf("want put a.1 To=3, got %v", cs)
	}
	// grow
	grow, _ := Diff(map[string]any{"a": []any{1}}, map[string]any{"a": []any{1, 2}})
	if c, ok := changeByPath(grow, "a.1"); !ok || c.Kind != chronicleschema.KindCreate || c.To != "2" {
		t.Fatalf("want create a.1, got %v", grow)
	}
	// shrink
	shrink, _ := Diff(map[string]any{"a": []any{1, 2}}, map[string]any{"a": []any{1}})
	if c, ok := changeByPath(shrink, "a.1"); !ok || c.Kind != chronicleschema.KindDelete {
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
		got, err := chronicleview.Reconstruct([]changelog.Commit{{Changes: build}, {Changes: change}})
		if err != nil {
			t.Fatalf("case %d reconstruct: %v", i, err)
		}
		if !reflect.DeepEqual(got, norm(t, tc.b)) {
			t.Fatalf("case %d round-trip mismatch:\n got  %#v\n want %#v", i, got, norm(t, tc.b))
		}
	}
}

// norm JSON-normalizes v for comparison with reconstructed values.
func norm(t *testing.T, v any) any {
	t.Helper()
	out, err := docmodel.Normalize(v)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	return out
}

// Strict mode exists because the positional fall back is silent and permanent:
// identity shapes what is RECORDED, so a forgotten WithIdentityFields cannot be
// repaired once the commits are sealed.
func TestDiff_StrictIdentity(t *testing.T) {
	before := map[string]any{"lines": []any{
		map[string]any{"id": "a", "qty": 1},
		map[string]any{"id": "b", "qty": 2},
	}}
	after := map[string]any{"lines": []any{
		map[string]any{"id": "b", "qty": 2},
		map[string]any{"id": "a", "qty": 1},
	}}

	// Undeclared identity: the default pairs positionally and records a reorder
	// as two edits — exactly the history strict mode refuses to write.
	cs, err := Diff(before, after)
	if err != nil {
		t.Fatalf("default must not error: %v", err)
	}
	if len(cs) == 0 {
		t.Fatal("default pairs positionally, so a reorder must record changes")
	}

	cs, err = Diff(before, after, chronicleschema.WithStrictIdentity())
	if !errors.Is(err, ErrNoIdentity) {
		t.Fatalf("strict + no identity: err = %v, want ErrNoIdentity", err)
	}
	if cs != nil {
		t.Fatalf("a refused diff must return no changes, got %v", cs)
	}
	if !strings.Contains(err.Error(), "lines") {
		t.Errorf("error must name the offending array: %v", err)
	}

	// Declared identity: strict mode is satisfied, and the reorder is a no-op.
	cs, err = Diff(before, after,
		chronicleschema.WithStrictIdentity(), chronicleschema.WithIdentityFields("id"))
	if err != nil {
		t.Fatalf("declared identity: %v", err)
	}
	if len(cs) != 0 {
		t.Fatalf("keyed pairing makes a reorder no change, got %v", cs)
	}
}

// TestDiff_ValueTypes_Money exercises WithValueTypes end to end: a money value
// declared as {amount, currency} diffs by its canonical scalar, so two
// differently-formatted amounts that mean the same value are not a change,
// while a genuine amount change is recorded as a Put of the canonical form.
func TestDiff_ValueTypes_Money(t *testing.T) {
	money := chronicleschema.ValueType{
		Fields: []string{"amount", "currency"},
		Canon: func(obj map[string]any) (string, bool) {
			amt, ok := obj["amount"].(string)
			if !ok {
				return "", false
			}
			cur, ok := obj["currency"].(string)
			if !ok {
				return "", false
			}
			f, err := strconv.ParseFloat(amt, 64)
			if err != nil {
				return "", false
			}
			return fmt.Sprintf("%q", fmt.Sprintf("%.2f %s", f, cur)), true // a JSON string scalar
		},
	}
	opt := chronicleschema.WithValueTypes(money)
	before := map[string]any{"price": map[string]any{"amount": "10.50", "currency": "USD"}}

	// Canon-equal: differently-formatted amounts, same canonical value.
	sameValue := map[string]any{"price": map[string]any{"amount": "10.5", "currency": "USD"}}
	cs, err := Diff(before, sameValue, opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 0 {
		t.Fatalf("canon-equal money must not diff, got %v", cs)
	}

	// Canon-different: a real amount change, recorded as the canonical scalars.
	changedValue := map[string]any{"price": map[string]any{"amount": "20.00", "currency": "USD"}}
	cs, err = Diff(before, changedValue, opt)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := changeByPath(cs, "price")
	if !ok || c.Kind != chronicleschema.KindPut || c.From != `"10.50 USD"` || c.To != `"20.00 USD"` {
		t.Fatalf("want put price From=\"10.50 USD\" To=\"20.00 USD\", got %v", cs)
	}
}

// TestDiff_ValueTypes_BadCanon: a Canon returning anything but a JSON scalar
// refuses the whole diff — sealing it would make the history unparseable on
// replay, and an invalid return is always a caller bug, never a choice.
func TestDiff_ValueTypes_BadCanon(t *testing.T) {
	bare := chronicleschema.ValueType{
		Fields: []string{"amount", "currency"},
		Canon:  func(obj map[string]any) (string, bool) { return "10.50 USD", true }, // not JSON
	}
	opt := chronicleschema.WithValueTypes(bare)
	price := map[string]any{"amount": "10.50", "currency": "USD"}

	// Create path (encode).
	if _, err := Diff(nil, map[string]any{"price": price}, opt); !errors.Is(err, ErrBadCanon) {
		t.Fatalf("create: err = %v, want ErrBadCanon", err)
	}
	// Compared-node put path.
	changed := map[string]any{"amount": "20.00", "currency": "USD"}
	if _, err := Diff(map[string]any{"price": price}, map[string]any{"price": changed}, opt); !errors.Is(err, ErrBadCanon) {
		t.Fatalf("put: err = %v, want ErrBadCanon", err)
	}
}

func TestDiff_StrictIdentity_NonObjectArrays(t *testing.T) {
	strict := chronicleschema.WithStrictIdentity()
	cases := map[string]struct{ before, after any }{
		// Scalars have no identity to lose; order IS the data.
		"scalars": {map[string]any{"tags": []any{"a", "b"}}, map[string]any{"tags": []any{"a", "c"}}},
		"empty":   {map[string]any{"lines": []any{}}, map[string]any{"lines": []any{}}},
		// A create never pairs anything, so nothing is at stake yet.
		"created": {map[string]any{}, map[string]any{"lines": []any{map[string]any{"id": "a"}}}},
	}
	for name, tc := range cases {
		if _, err := Diff(tc.before, tc.after, strict); err != nil {
			t.Errorf("%s: strict mode must allow this: %v", name, err)
		}
	}
}
