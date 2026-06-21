package chroniclekit

import (
	"context"
	"encoding/json"
	"fmt"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
)

// Reconstruct replays commits (OLDEST first) into a document state, applying each
// change in order: put/create set the value at its path, delete removes it.
// Intermediate containers are created as needed (numeric segments make arrays).
func Reconstruct(commits []changelog.Commit) (map[string]any, error) {
	root := map[string]any{}
	var cur any = root
	for _, c := range commits {
		for _, ch := range c.Changes {
			next, err := applyChange(cur, ch)
			if err != nil {
				return nil, fmt.Errorf("reconstruct %s %q: %w", ch.Kind, ch.Path, err)
			}
			cur = next
		}
	}
	m, ok := cur.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("reconstruct: document root is not an object (got %T)", cur)
	}
	return m, nil
}

func applyChange(root any, ch changelog.Change) (any, error) {
	segs := splitPath(ch.Path)
	if len(segs) == 0 {
		return nil, fmt.Errorf("empty path")
	}
	if ch.Kind == KindDelete {
		return deleteIn(root, segs), nil
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
		idx, ok := asIndex(seg)
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
		if _, ok := asIndex(seg); ok {
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
		idx, ok := asIndex(seg)
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

// getAt navigates root to segs (dispatching on runtime type, so numeric object
// keys resolve correctly), returning the value and whether it was found.
func getAt(root map[string]any, segs []string) (any, bool) {
	var cur any = root
	for _, s := range segs {
		switch c := cur.(type) {
		case []any:
			idx, ok := asIndex(s)
			if !ok || idx >= len(c) {
				return nil, false
			}
			cur = c[idx]
		case map[string]any:
			v, present := c[s]
			if !present {
				return nil, false
			}
			cur = v
		default:
			return nil, false
		}
	}
	return cur, true
}

// lcaPath returns the lowest common ancestor of paths — the longest common path
// prefix, compared SEGMENT-WISE (not character-wise, and not LCS). "" means the
// only common ancestor is the document root.
//
//	items.0.qty + items.0.price → items.0
//	items.0.qty + items.2.price → items
//	status      + items.0.qty   → ""        (root)
func lcaPath(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	prefix := splitPath(paths[0])
	for _, p := range paths[1:] {
		segs := splitPath(p)
		j := 0
		for j < len(prefix) && j < len(segs) && prefix[j] == segs[j] {
			j++
		}
		prefix = prefix[:j]
		if len(prefix) == 0 {
			return ""
		}
	}
	return joinPath(prefix)
}

// State reconstructs docID's current state at HEAD.
func (k *Kit) State(ctx context.Context, docID string) (map[string]any, error) {
	commits, err := k.svc.Commits(ctx, docID, 0) // newest-first
	if err != nil {
		return nil, err
	}
	return Reconstruct(reversed(commits))
}

// StateAt reconstructs docID's state as of (and including) commitID. An empty
// commitID yields the empty document (the state before the root commit).
func (k *Kit) StateAt(ctx context.Context, docID, commitID string) (map[string]any, error) {
	commits, err := k.svc.Commits(ctx, docID, 0)
	if err != nil {
		return nil, err
	}
	return stateUpTo(commits, commitID)
}

// stateUpTo replays commits (given NEWEST-first, as core returns) up to and
// including commitID. An empty commitID yields the empty document. It errors if a
// non-empty commitID is not present (rather than silently returning HEAD state).
func stateUpTo(commits []changelog.Commit, commitID string) (map[string]any, error) {
	chrono := reversed(commits)
	upto := make([]changelog.Commit, 0, len(chrono))
	if commitID != "" {
		found := false
		for _, c := range chrono {
			upto = append(upto, c)
			if c.ID == commitID {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("chroniclekit: commit %q not found", commitID)
		}
	}
	return Reconstruct(upto)
}

// CommitSnapshot returns the per-commit LCA snapshot: the before-state (as of the
// commit's parent) of the smallest subtree containing every change in the commit.
// Scope = lcaPath of the commit's changed paths; if that scope is a scalar/leaf
// or missing in the parent state, it climbs to the enclosing container. When
// changes scatter (LCA = root) the snapshot is the whole prior document.
func (k *Kit) CommitSnapshot(ctx context.Context, docID, commitID string) (any, error) {
	commits, err := k.svc.Commits(ctx, docID, 0)
	if err != nil {
		return nil, err
	}
	var target *changelog.Commit
	for i := range commits {
		if commits[i].ID == commitID {
			target = &commits[i]
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("chroniclekit: commit %q not found in %q", commitID, docID)
	}
	before, err := stateUpTo(commits, target.Parent) // reuse the already-fetched list
	if err != nil {
		return nil, err
	}
	paths := make([]string, len(target.Changes))
	for i, c := range target.Changes {
		paths[i] = c.Path
	}
	scope := lcaPath(paths)
	val, ok := getAt(before, splitPath(scope))
	for scope != "" && (!ok || !isContainer(val)) {
		scope = parentPath(scope)
		val, ok = getAt(before, splitPath(scope))
	}
	if scope == "" {
		return before, nil // root → whole prior document
	}
	return val, nil
}

// reversed returns commits in chronological (oldest-first) order. core's Commits
// returns newest-first; Reconstruct needs oldest-first.
func reversed(commits []changelog.Commit) []changelog.Commit {
	out := make([]changelog.Commit, len(commits))
	for i, c := range commits {
		out[len(commits)-1-i] = c
	}
	return out
}
