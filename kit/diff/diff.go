// Package chroniclediff is the kit's write side: it turns a before/after pair
// of documents into the Changes that record the edit.
//
// The caller's document shape is declared with chronicleschema Options —
// element identity is the one that matters here, because it shapes what is
// RECORDED and a later declaration cannot re-key commits already sealed.
package chroniclediff

import (
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
	"github.com/zdirnecamlcs96/chronicle/kit/internal/docmodel"
	chronicleschema "github.com/zdirnecamlcs96/chronicle/kit/schema"
)

// ErrNoIdentity is returned under chronicleschema.WithStrictIdentity when an
// array of objects has no usable element identity and would therefore pair
// positionally. Only that option produces it; the default remains a silent
// positional fall back.
var ErrNoIdentity = errors.New("chroniclediff: array of objects has no usable element identity")

// ErrBadCanon is returned when a ValueType.Canon accepts an object but returns
// something that is not a JSON scalar. Sealing such a value would make the
// history unparseable on replay, and an invalid return is always a caller bug,
// so the diff refuses unconditionally.
var ErrBadCanon = errors.New("chroniclediff: value type Canon must return a JSON scalar")

// Diff compares two states and returns the Changes that turn before into after.
// Both are JSON-normalized first (marshal then unmarshal into any), so structs
// (honouring json tags) and maps diff uniformly. From/To hold the canonical-JSON
// before/after leaf values; "" means the value is absent on that side (From on a
// create, To on a delete). Diff(x, x) returns no changes.
//
// Arrays of objects pair by element identity when available, walking a chain
// per array: the caller-configured key (WithArrayKeys) → a generic identity
// field declared by WithIdentityFields, holding a unique scalar on every
// element of both sides → positional (by index). Scalar and mixed arrays
// always pair positionally: a mid-array insertion reads as a run of puts plus
// a tail create. WithStrictIdentity makes that last hop an ErrNoIdentity for
// arrays of objects, where falling back to index is usually an oversight rather
// than a choice. Keyed pairing trades order for identity — reordering alone
// records nothing, so replaying the changes reproduces the element SET
// (survivors in before order, additions appended), not the after ordering; use
// positional arrays where order is meaningful data.
//
// WithValueTypes records declared value-object shapes as their canonical
// scalar (replay yields the scalar, not the object); a Canon returning
// anything but a JSON scalar is an ErrBadCanon. Bookkeeping fields are a
// read-side concern: Diff records every field — the changelog is the data
// record — and WithIgnoredFields only flags their changes on Explain. With no
// options, arrays without a usable identity and all other values diff exactly
// as before.
//
// States are typically object- or array-rooted (the CRUD norm). A scalar root
// produces a single change with an empty Path — a whole-root replace, which
// chronicleview.Reconstruct applies like any other.
func Diff(before, after any, opts ...chronicleschema.Option) ([]changelog.Change, error) {
	b, err := docmodel.Normalize(before)
	if err != nil {
		return nil, err
	}
	a, err := docmodel.Normalize(after)
	if err != nil {
		return nil, err
	}
	// Top-level only: coerce a genuinely-absent (nil) root against a container so a
	// whole-document create/delete diffs into per-key changes. Nested present-null
	// is NOT coerced here — it flows through value's leaf branch as a put to/from
	// "null", preserving null as a distinct value.
	b, a = coerceRoot(b, a)
	d := &differ{cfg: chronicleschema.New(opts...)}
	var out []changelog.Change
	d.value(nil, nil, b, a, &out)
	if d.err != nil {
		return nil, d.err
	}
	return out, nil
}

// differ carries the caller-declared schema through the walk.
type differ struct {
	cfg chronicleschema.Config
	err error // first strict-mode or canon violation; the walk itself cannot fail
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

// value dispatches one before/after pair. path is the machine path (indices
// included); schema is the index-free field path used for array-key and label
// lookups.
func (d *differ) value(path, schema []string, before, after any, out *[]changelog.Change) {
	// A declared value-object on either side compares canonically as one leaf:
	// different encodings of the same value are not changes, and a real change
	// records the canonical form, not the object.
	if bc, bVT := d.canon(before); bVT {
		if ac := d.encode(after); bc != ac {
			*out = append(*out, changelog.Change{Path: docmodel.JoinPath(path), Kind: chronicleschema.KindPut, From: bc, To: ac})
		}
		return
	}
	if ac, aVT := d.canon(after); aVT {
		if bc := docmodel.CanonJSON(before); bc != ac {
			*out = append(*out, changelog.Change{Path: docmodel.JoinPath(path), Kind: chronicleschema.KindPut, From: bc, To: ac})
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
	// whole-value replace; docmodel.Apply replays it correctly, including at the
	// document root (an empty Path there means "replace the whole root value").
	// A present JSON null reaches here and round-trips as To/From "null"
	// (distinct from absence, which object/array express as create/delete).
	if !jsonEqual(before, after) {
		*out = append(*out, changelog.Change{
			Path: docmodel.JoinPath(path),
			Kind: chronicleschema.KindPut,
			From: docmodel.CanonJSON(before),
			To:   docmodel.CanonJSON(after),
		})
	}
}

func (d *differ) object(path, schema []string, before, after map[string]any, out *[]changelog.Change) {
	for _, k := range unionKeys(before, after) {
		bv, bok := before[k]
		av, aok := after[k]
		// An empty-string key collides with the root path in docmodel's path
		// grammar (JoinPath of a lone "" segment equals JoinPath of no segments),
		// so it can never be recorded unambiguously at any depth. Skip it rather
		// than seal a Change docmodel.Apply cannot replay — but only warn when
		// the skip actually suppresses a real difference.
		if k == "" {
			if bok != aok || !jsonEqual(bv, av) {
				log.Printf("chroniclediff: skipping empty-string object key at %q", docmodel.JoinPath(path))
			}
			continue
		}
		child := docmodel.ChildPath(path, k)
		switch {
		case bok && aok:
			d.value(child, docmodel.ChildPath(schema, k), bv, av, out)
		case aok: // created
			*out = append(*out, changelog.Change{Path: docmodel.JoinPath(child), Kind: chronicleschema.KindCreate, To: d.encode(av)})
		default: // deleted
			*out = append(*out, changelog.Change{Path: docmodel.JoinPath(child), Kind: chronicleschema.KindDelete, From: d.encode(bv)})
		}
	}
}

func (d *differ) array(path, schema []string, before, after []any, out *[]changelog.Change) {
	// Arrays of objects may carry element identity (caller-declared key, else a
	// declared generic field); everything else — and any array where identity is
	// unusable — pairs by index. A candidate key is usable only when every
	// element on BOTH sides is an object holding a unique scalar at the key
	// path; the result shapes what is recorded, so it must never depend on
	// anything the caller did not declare.
	if keyPath, ok := d.cfg.IdentityKey(schema, before, after); ok {
		d.keyedArray(path, schema, keyPath, before, after, out)
		return
	}
	// Strict mode: pairing an array of objects by index is only ever a
	// deliberate choice, never a safe default, so refuse the whole diff rather
	// than seal a history that cannot be re-keyed later. Scalar and empty
	// arrays have no identity to lose and pass.
	if d.err == nil && d.cfg.StrictIdentity() && anyObjectElem(before, after) {
		name := docmodel.JoinPath(schema)
		if name == "" {
			name = "(root)"
		}
		d.err = fmt.Errorf("%w: %s", ErrNoIdentity, name)
	}
	n := len(before)
	if len(after) < n {
		n = len(after)
	}
	for i := 0; i < n; i++ {
		d.value(docmodel.ChildPath(path, strconv.Itoa(i)), schema, before[i], after[i], out)
	}
	for i := n; i < len(after); i++ {
		*out = append(*out, changelog.Change{Path: docmodel.JoinPath(docmodel.ChildPath(path, strconv.Itoa(i))), Kind: chronicleschema.KindCreate, To: d.encode(after[i])})
	}
	// Trailing deletes are emitted HIGH→LOW: applying a delete shifts later
	// elements left, so deleting ascending would invalidate each subsequent
	// index. Descending is shift-safe.
	for i := len(before) - 1; i >= n; i-- {
		*out = append(*out, changelog.Change{Path: docmodel.JoinPath(docmodel.ChildPath(path, strconv.Itoa(i))), Kind: chronicleschema.KindDelete, From: d.encode(before[i])})
	}
}

// keyedArray pairs elements by identity instead of position. Reordering alone
// is not a change; replay therefore reproduces the element SET (survivors in
// before order, additions appended), not the after ordering. Emission order
// is replay order: deletes by before-index descending (a delete shifts left),
// then survivor edits ascending at their post-delete indices, then creates
// appended (a set at index == length appends).
func (d *differ) keyedArray(path, schema, keyPath []string, before, after []any, out *[]changelog.Change) {
	keyOf := func(el any) string {
		v, _ := docmodel.ElemKeyValue(el.(map[string]any), keyPath)
		return docmodel.CanonJSON(v)
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
			*out = append(*out, changelog.Change{Path: docmodel.JoinPath(docmodel.ChildPath(path, strconv.Itoa(i))), Kind: chronicleschema.KindDelete, From: d.encode(before[i])})
		}
	}

	removedBelow := 0
	for i, el := range before {
		if deleted[i] {
			removedBelow++
			continue
		}
		d.value(docmodel.ChildPath(path, strconv.Itoa(i-removedBelow)), schema, el, after[aIdx[bKeys[i]]], out)
	}

	next := len(before) - removedBelow
	for _, el := range after {
		if _, existed := bIdx[keyOf(el)]; !existed {
			*out = append(*out, changelog.Change{Path: docmodel.JoinPath(docmodel.ChildPath(path, strconv.Itoa(next))), Kind: chronicleschema.KindCreate, To: d.encode(el)})
			next++
		}
	}
}

// canon is Config.Canon with the return validated: an accepted object whose
// canonical form is not a JSON scalar refuses the diff (ErrBadCanon).
func (d *differ) canon(v any) (string, bool) {
	s, ok := d.cfg.Canon(v)
	if ok && !docmodel.IsJSONScalar(s) && d.err == nil {
		d.err = fmt.Errorf("%w: %q", ErrBadCanon, s)
	}
	return s, ok
}

// encode is canonical JSON with value-type awareness: declared shapes encode as
// their canonical scalar.
func (d *differ) encode(v any) string {
	if s, ok := d.canon(v); ok {
		return s
	}
	return docmodel.CanonJSON(v)
}

// anyObjectElem reports whether either side holds an object element — the only
// arrays where positional pairing can silently lose element identity.
func anyObjectElem(sides ...[]any) bool {
	for _, side := range sides {
		for _, el := range side {
			if _, ok := el.(map[string]any); ok {
				return true
			}
		}
	}
	return false
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

func jsonEqual(a, b any) bool {
	return docmodel.CanonJSON(a) == docmodel.CanonJSON(b)
}
