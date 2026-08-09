package docmodel

import (
	"encoding/json"
	"fmt"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
)

// Kinds the kit emits. Free-form in core; the kit fixes this small vocabulary
// so Diff output and Apply agree. chronicleschema re-exports these — that is
// the name a caller writes.
const (
	KindCreate = "create" // a path that did not exist before
	KindPut    = "put"    // a leaf value changed
	KindDelete = "delete" // a path that no longer exists
)

// Apply applies one Change to root and returns the (possibly rebound) root
// container. A Kind outside the kit vocabulary is an error — silently guessing
// would corrupt the reconstruction.
func Apply(root any, ch changelog.Change) (any, error) {
	segs := SplitPath(ch.Path)
	if len(segs) == 0 {
		return nil, fmt.Errorf("empty path")
	}
	switch ch.Kind {
	case KindDelete:
		return deleteIn(root, segs), nil
	case KindCreate, KindPut:
		// fall through to set below
	default:
		return nil, fmt.Errorf("unknown change kind %q", ch.Kind)
	}
	var val any
	if ch.To != "" {
		if err := json.Unmarshal([]byte(ch.To), &val); err != nil {
			return nil, err
		}
	}
	return setIn(root, segs, val), nil
}

// setIn sets val at segs within cur. The container kind is decided by cur's
// RUNTIME type — an existing map treats a numeric segment as a string key (not an
// index), so objects with numeric keys are preserved. Only when a container is
// absent is it vivified by segment shape (numeric → array, else object). It
// returns the (possibly new or grown) container so the caller can rebind it.
func setIn(cur any, segs []string, val any) any {
	seg := segs[0]
	last := len(segs) == 1
	switch c := cur.(type) {
	case []any:
		idx, ok := AsIndex(seg)
		if !ok {
			// path says object key but container is an array — replace with a map.
			return setIn(map[string]any{}, segs, val)
		}
		for len(c) <= idx {
			c = append(c, nil)
		}
		if last {
			c[idx] = val
		} else {
			c[idx] = setIn(c[idx], segs[1:], val)
		}
		return c
	case map[string]any:
		if last {
			c[seg] = val
		} else {
			c[seg] = setIn(c[seg], segs[1:], val)
		}
		return c
	default: // absent: vivify by segment shape
		if _, ok := AsIndex(seg); ok {
			return setIn([]any{}, segs, val)
		}
		return setIn(map[string]any{}, segs, val)
	}
}

// deleteIn removes the value at segs within cur, dispatching on cur's runtime
// type. A mid-array index deletion shifts later elements (positional semantics).
func deleteIn(cur any, segs []string) any {
	seg := segs[0]
	last := len(segs) == 1
	switch c := cur.(type) {
	case []any:
		idx, ok := AsIndex(seg)
		if !ok || idx >= len(c) {
			return c
		}
		if last {
			return append(c[:idx], c[idx+1:]...)
		}
		c[idx] = deleteIn(c[idx], segs[1:])
		return c
	case map[string]any:
		if last {
			delete(c, seg)
		} else if child, present := c[seg]; present {
			c[seg] = deleteIn(child, segs[1:])
		}
		return c
	default:
		return cur
	}
}
