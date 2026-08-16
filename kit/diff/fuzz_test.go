package chroniclediff

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/zdirnecamlcs96/chronicle/kit/internal/docmodel"
)

// FuzzDiffApplyRoundTrip checks the core write-side property: replaying
// Diff(before, after) onto before, one Change at a time via docmodel.Apply,
// reproduces after exactly. docmodel.Apply now handles an empty Path as a
// whole-root replace, so the property holds for every JSON root shape
// (object, array, or scalar) — no root-shape skip needed.
func FuzzDiffApplyRoundTrip(f *testing.F) {
	seeds := []struct{ before, after string }{
		{`{"a":1}`, `{"a":2}`},                         // flat object put
		{`{"x":{"y":1,"z":9}}`, `{"x":{"y":2,"z":9}}`}, // nested object
		{`{"items":[{"sku":"A","qty":1}]}`, `{"items":[{"sku":"A","qty":5},{"sku":"B","qty":2}]}`}, // array of objects (positional pairing, no identity declared)
		{`{"lines":[{"id":"a","qty":1},{"id":"b","qty":2}]}`, `{"lines":[{"id":"a","qty":9}]}`},    // array shrink (tests descending delete order)
		{`{"a":1}`, `{"a":null}`},            // value -> null
		{`{"a":null}`, `{"a":1}`},            // null -> value
		{`{}`, `{"a":1,"b":{"c":2}}`},        // empty -> populated
		{`{"a":1,"b":{"c":2}}`, `{}`},        // populated -> empty
		{`{"n":1}`, `{"n":1.0}`},             // numeric edge: int vs float literal
		{`{"n":1}`, `{"n":100000000000000}`}, // large int

		// Was a fuzz-found failure (before=`{}`, after=`{"":{}}`); now expected
		// behavior, not a crash. An empty-string object key at a position whose
		// path would otherwise be "" collides with the root path in docmodel's
		// grammar, so Diff (kit/diff/diff.go, object()) now skips and warns on any
		// "" key rather than recording it. stripEmptyKeys below removes such keys
		// from both sides before the property check, per that policy.
		{`{}`, `{"":{}}`},

		// Was a fuzz-found failure (before=`[]`, after=`{}`); now fixed and
		// promoted to a real seed. A root-level container-TYPE change has no
		// ""-keyed value anywhere; Diff's fallback whole-value-replace branch
		// (kit/diff/diff.go, differ.value) emits {Kind:put, Path:""} for it, and
		// docmodel.Apply now treats an empty Path as "replace the whole root"
		// instead of rejecting it.
		{`[]`, `{}`},
		{`{}`, `[]`},
		{`1`, `{"a":1}`}, // scalar root -> object root
		{`"x"`, `"y"`},   // scalar root -> scalar root
	}
	for _, s := range seeds {
		f.Add(s.before, s.after)
	}

	f.Fuzz(func(t *testing.T, beforeJSON, afterJSON string) {
		var beforeRaw, afterRaw any
		if err := json.Unmarshal([]byte(beforeJSON), &beforeRaw); err != nil {
			t.Skip()
		}
		if err := json.Unmarshal([]byte(afterJSON), &afterRaw); err != nil {
			t.Skip()
		}

		before, err := docmodel.Normalize(beforeRaw)
		if err != nil {
			t.Skip()
		}
		after, err := docmodel.Normalize(afterRaw)
		if err != nil {
			t.Skip()
		}
		// Empty-string object keys are policy-excluded from recording (Diff warns
		// and skips them, at any depth) — outside the round-trip contract, so
		// strip them from both sides before checking the property.
		before, after = stripEmptyKeys(before), stripEmptyKeys(after)

		changes, err := Diff(before, after)
		if err != nil {
			t.Fatalf("Diff: %v", err)
		}

		root := before
		for _, ch := range changes {
			root, err = docmodel.Apply(root, ch)
			if err != nil {
				t.Fatalf("Apply(%+v): %v", ch, err)
			}
		}

		if !reflect.DeepEqual(root, after) {
			t.Fatalf("round trip mismatch:\n before  %#v\n after   %#v\n replayed %#v\n changes %+v", before, after, root, changes)
		}
	})
}

// stripEmptyKeys recursively removes ""-keyed map entries, mirroring Diff's
// policy of never recording an empty-string object key.
func stripEmptyKeys(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, cv := range t {
			if k == "" {
				continue
			}
			out[k] = stripEmptyKeys(cv)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, cv := range t {
			out[i] = stripEmptyKeys(cv)
		}
		return out
	default:
		return v
	}
}
