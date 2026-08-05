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
// Arrays of objects pair by element identity when available, walking a chain
// per array: the caller-configured key (WithArrayKeys) → a unique scalar "id"
// field on every element of both sides → positional (by index). Scalar and
// mixed arrays always pair positionally: a mid-array insertion reads as a run
// of puts plus a tail create. Keyed pairing trades order for identity —
// reordering alone records nothing, so replaying the changes reproduces the
// element SET (survivors in before order, additions appended), not the after
// ordering; use positional arrays where order is meaningful data.
//
// WithValueTypes records declared value-object shapes as their canonical
// scalar (replay yields the scalar, not the object). Bookkeeping fields are a
// read-side concern: Diff records every field — the changelog is the data
// record — and WithIgnoredFields only flags their changes on Explain. With no
// options, arrays without a usable id and all other values diff exactly as
// before.
//
// States are expected to be object- or array-rooted (the CRUD norm). A scalar
// root produces a single change with an empty Path, which Reconstruct does not
// apply; diff object/array documents.
func Diff(before, after any, opts ...DiffOption) ([]changelog.Change, error) {
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
	d := &differ{cfg: newDiffConfig(opts)}
	var out []changelog.Change
	d.value(nil, nil, b, a, &out)
	return out, nil
}

// differ carries the caller-declared schema through the walk.
type differ struct {
	cfg diffConfig
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

// value dispatches one before/after pair. path is the machine path (indices
// included); schema is the index-free field path used for arrayKeys and label
// lookups.
func (d *differ) value(path, schema []string, before, after any, out *[]changelog.Change) {
	// A declared value-object on either side compares canonically as one leaf:
	// different encodings of the same value are not changes, and a real change
	// records the canonical form, not the object.
	if bc, bVT := d.canonOf(before); bVT {
		if ac := d.encode(after); bc != ac {
			*out = append(*out, changelog.Change{Path: joinPath(path), Kind: KindPut, From: bc, To: ac})
		}
		return
	}
	if ac, aVT := d.canonOf(after); aVT {
		if bc := mustJSON(before); bc != ac {
			*out = append(*out, changelog.Change{Path: joinPath(path), Kind: KindPut, From: bc, To: ac})
		}
		return
	}

	bObj, bIsObj := before.(map[string]any)
	aObj, aIsObj := after.(map[string]any)
	if bIsObj && aIsObj {
		d.object(path, schema, bObj, aObj, out)
		return
	}

	bArr, bIsArr := before.([]any)
	aArr, aIsArr := after.([]any)
	if bIsArr && aIsArr {
		d.array(path, schema, bArr, aArr, out)
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

func (d *differ) object(path, schema []string, before, after map[string]any, out *[]changelog.Change) {
	for _, k := range unionKeys(before, after) {
		bv, bok := before[k]
		av, aok := after[k]
		child := childPath(path, k)
		switch {
		case bok && aok:
			d.value(child, childPath(schema, k), bv, av, out)
		case aok: // created
			*out = append(*out, changelog.Change{Path: joinPath(child), Kind: KindCreate, To: d.encode(av)})
		default: // deleted
			*out = append(*out, changelog.Change{Path: joinPath(child), Kind: KindDelete, From: d.encode(bv)})
		}
	}
}

func (d *differ) array(path, schema []string, before, after []any, out *[]changelog.Change) {
	// Arrays of objects may carry element identity (caller-declared key, else
	// the "id" convention); everything else — and any array where identity is
	// unusable — pairs by index.
	if keyPath, ok := d.resolveKey(schema, before, after); ok {
		d.keyedArray(path, schema, keyPath, before, after, out)
		return
	}
	n := len(before)
	if len(after) < n {
		n = len(after)
	}
	for i := 0; i < n; i++ {
		d.value(childPath(path, strconv.Itoa(i)), schema, before[i], after[i], out)
	}
	for i := n; i < len(after); i++ {
		*out = append(*out, changelog.Change{Path: joinPath(childPath(path, strconv.Itoa(i))), Kind: KindCreate, To: d.encode(after[i])})
	}
	// Trailing deletes are emitted HIGH→LOW: deleteIn shifts later elements left,
	// so deleting ascending would invalidate each subsequent index. Descending is
	// shift-safe.
	for i := len(before) - 1; i >= n; i-- {
		*out = append(*out, changelog.Change{Path: joinPath(childPath(path, strconv.Itoa(i))), Kind: KindDelete, From: d.encode(before[i])})
	}
}

// resolveKey picks the identity key path for the array at schema, walking the
// chain: caller-configured key → the generic "id" convention → none
// (positional). A candidate is usable only when every element on both sides
// is an object holding a unique scalar at the key path; an empty side passes
// vacuously.
func (d *differ) resolveKey(schema []string, before, after []any) ([]string, bool) {
	if cfgPath, ok := d.cfg.arrayKeys[joinPath(schema)]; ok {
		kp := splitPath(cfgPath)
		if usableKey(kp, before) && usableKey(kp, after) {
			return kp, true
		}
	}
	kp := []string{"id"}
	if usableKey(kp, before) && usableKey(kp, after) {
		return kp, true
	}
	return nil, false
}

func usableKey(keyPath []string, side []any) bool {
	seen := make(map[string]struct{}, len(side))
	for _, el := range side {
		obj, isObj := el.(map[string]any)
		if !isObj {
			return false
		}
		v, ok := elemKeyValue(obj, keyPath)
		if !ok || v == nil || isContainer(v) {
			return false
		}
		k := mustJSON(v)
		if _, dup := seen[k]; dup {
			return false
		}
		seen[k] = struct{}{}
	}
	return true
}

// elemKeyValue walks keyPath through nested objects only (an identity path
// never crosses an array).
func elemKeyValue(el map[string]any, keyPath []string) (any, bool) {
	var cur any = el
	for _, seg := range keyPath {
		obj, isObj := cur.(map[string]any)
		if !isObj {
			return nil, false
		}
		var ok bool
		if cur, ok = obj[seg]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// keyedArray pairs elements by identity instead of position. Reordering alone
// is not a change; replay therefore reproduces the element SET (survivors in
// before order, additions appended), not the after ordering. Emission order
// is replay order: deletes by before-index descending (deleteIn shifts left),
// then survivor edits ascending at their post-delete indices, then creates
// appended (setIn at index == length appends).
func (d *differ) keyedArray(path, schema, keyPath []string, before, after []any, out *[]changelog.Change) {
	keyOf := func(el any) string {
		v, _ := elemKeyValue(el.(map[string]any), keyPath)
		return mustJSON(v)
	}
	aIdx := make(map[string]int, len(after))
	for j, el := range after {
		aIdx[keyOf(el)] = j
	}
	bKeys := make([]string, len(before))
	bIdx := make(map[string]int, len(before))
	for i, el := range before {
		bKeys[i] = keyOf(el)
		bIdx[bKeys[i]] = i
	}

	deleted := make([]bool, len(before))
	for i := len(before) - 1; i >= 0; i-- {
		if _, survives := aIdx[bKeys[i]]; !survives {
			deleted[i] = true
			*out = append(*out, changelog.Change{Path: joinPath(childPath(path, strconv.Itoa(i))), Kind: KindDelete, From: d.encode(before[i])})
		}
	}

	removedBelow := 0
	for i, el := range before {
		if deleted[i] {
			removedBelow++
			continue
		}
		d.value(childPath(path, strconv.Itoa(i-removedBelow)), schema, el, after[aIdx[bKeys[i]]], out)
	}

	next := len(before) - removedBelow
	for _, el := range after {
		if _, existed := bIdx[keyOf(el)]; !existed {
			*out = append(*out, changelog.Change{Path: joinPath(childPath(path, strconv.Itoa(next))), Kind: KindCreate, To: d.encode(el)})
			next++
		}
	}
}

// canonOf returns the canonical scalar for v when v is an object whose key
// set exactly matches a declared ValueType and its Canon accepts. The first
// key-set match decides; a declined Canon means normal field-wise diffing.
func (d *differ) canonOf(v any) (string, bool) {
	obj, isObj := v.(map[string]any)
	if !isObj {
		return "", false
	}
outer:
	for _, vt := range d.cfg.valueTypes {
		if len(vt.Fields) != len(obj) {
			continue
		}
		for _, f := range vt.Fields {
			if _, present := obj[f]; !present {
				continue outer
			}
		}
		return vt.Canon(obj)
	}
	return "", false
}

// encode is mustJSON with value-type awareness: declared shapes encode as
// their canonical scalar.
func (d *differ) encode(v any) string {
	if s, ok := d.canonOf(v); ok {
		return s
	}
	return mustJSON(v)
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
