package chroniclekit

import (
	"encoding/json"
	"sort"
	"strconv"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
)

// Kinds Diff emits. Free-form in core; the kit fixes this small vocabulary so
// Diff output and Reconstruct apply agree.
const (
	KindCreate = "create" // a path that did not exist before
	KindPut    = "put"    // a leaf value changed
	KindDelete = "delete" // a path that no longer exists
)

// Diff compares two states and returns the Changes that turn before into after.
// Both are JSON-normalized first (marshal then unmarshal into any), so structs
// (honouring json tags) and maps diff uniformly. From/To hold the canonical-JSON
// before/after leaf values; "" means the value is absent on that side (From on a
// create, To on a delete). Diff(x, x) returns no changes.
//
// Arrays diff positionally (by index): a mid-array insertion reads as a run of
// puts plus a tail create — a documented v1 limitation, not a correctness bug for
// reconstruction.
//
// States are expected to be object- or array-rooted (the CRUD norm). A scalar
// root produces a single change with an empty Path, which Reconstruct does not
// apply; diff object/array documents.
func Diff(before, after any) ([]changelog.Change, error) {
	b, err := normalize(before)
	if err != nil {
		return nil, err
	}
	a, err := normalize(after)
	if err != nil {
		return nil, err
	}
	// Top-level only: coerce a genuinely-absent (nil) root against a container so a
	// whole-document create/delete diffs into per-key changes. Nested present-null
	// is NOT coerced here — it flows through diffValue's leaf branch as a put to/from
	// "null", preserving null as a distinct value.
	b, a = coerceRoot(b, a)
	var out []changelog.Change
	diffValue(nil, b, a, &out)
	return out, nil
}

func coerceRoot(before, after any) (any, any) {
	if before == nil {
		if _, ok := after.(map[string]any); ok {
			before = map[string]any{}
		} else if _, ok := after.([]any); ok {
			before = []any{}
		}
	}
	if after == nil {
		if _, ok := before.(map[string]any); ok {
			after = map[string]any{}
		} else if _, ok := before.([]any); ok {
			after = []any{}
		}
	}
	return before, after
}

func normalize(v any) (any, error) {
	bs, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(bs, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func diffValue(path []string, before, after any, out *[]changelog.Change) {
	bObj, bIsObj := before.(map[string]any)
	aObj, aIsObj := after.(map[string]any)
	if bIsObj && aIsObj {
		diffObject(path, bObj, aObj, out)
		return
	}

	bArr, bIsArr := before.([]any)
	aArr, aIsArr := after.([]any)
	if bIsArr && aIsArr {
		diffArray(path, bArr, aArr, out)
		return
	}

	// Leaf, or any container-type change (object↔array↔scalar↔null) handled as a
	// whole-value replace; setIn applies it correctly on reconstruct. A present
	// JSON null reaches here and round-trips as To/From "null" (distinct from
	// absence, which diffObject/diffArray express as create/delete).
	if !jsonEqual(before, after) {
		*out = append(*out, changelog.Change{
			Path: joinPath(path),
			Kind: KindPut,
			From: mustJSON(before),
			To:   mustJSON(after),
		})
	}
}

func diffObject(path []string, before, after map[string]any, out *[]changelog.Change) {
	for _, k := range unionKeys(before, after) {
		bv, bok := before[k]
		av, aok := after[k]
		child := childPath(path, k)
		switch {
		case bok && aok:
			diffValue(child, bv, av, out)
		case aok: // created
			*out = append(*out, changelog.Change{Path: joinPath(child), Kind: KindCreate, To: mustJSON(av)})
		default: // deleted
			*out = append(*out, changelog.Change{Path: joinPath(child), Kind: KindDelete, From: mustJSON(bv)})
		}
	}
}

func diffArray(path []string, before, after []any, out *[]changelog.Change) {
	n := len(before)
	if len(after) < n {
		n = len(after)
	}
	for i := 0; i < n; i++ {
		diffValue(childPath(path, strconv.Itoa(i)), before[i], after[i], out)
	}
	for i := n; i < len(after); i++ {
		*out = append(*out, changelog.Change{Path: joinPath(childPath(path, strconv.Itoa(i))), Kind: KindCreate, To: mustJSON(after[i])})
	}
	// Trailing deletes are emitted HIGH→LOW: deleteIn shifts later elements left,
	// so deleting ascending would invalidate each subsequent index. Descending is
	// shift-safe.
	for i := len(before) - 1; i >= n; i-- {
		*out = append(*out, changelog.Change{Path: joinPath(childPath(path, strconv.Itoa(i))), Kind: KindDelete, From: mustJSON(before[i])})
	}
}

// childPath returns a fresh slice path+[seg] (never aliases the parent's array).
func childPath(path []string, seg string) []string {
	c := make([]string, len(path)+1)
	copy(c, path)
	c[len(path)] = seg
	return c
}

func unionKeys(a, b map[string]any) []string {
	seen := map[string]struct{}{}
	for k := range a {
		seen[k] = struct{}{}
	}
	for k := range b {
		seen[k] = struct{}{}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic change order
	return keys
}

// mustJSON canonical-encodes v; an absent value is signalled by the caller using
// "" (this returns "null" for a present JSON null). json.Marshal sorts map keys,
// so output is deterministic.
func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func jsonEqual(a, b any) bool { return mustJSON(a) == mustJSON(b) }
