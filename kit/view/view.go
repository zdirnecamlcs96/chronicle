// Package chronicleview is the kit's read side: it replays recorded Changes
// back into document state.
//
// Reconstruct is the plain fold over a chain. Reader adds the accelerated
// paths — when the backend exposes core's optional Snapshotter and TailReader
// (detected once, in New), a read serves from the stored snapshot plus a tail
// replay instead of refetching the whole history. The snapshot is a pure
// cache: deleting stored snapshots is always safe, and every fast path falls
// back to a full rebuild rather than erroring.
package chronicleview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
	"github.com/zdirnecamlcs96/chronicle/kit/internal/docmodel"
)

// Reader serves document state over a changelog.Service, using the backend's
// snapshot and cursor capabilities when it has them.
type Reader struct {
	svc changelog.Service

	// Optional backend capabilities discovered in New; both nil is fine and
	// simply means State reads by full replay.
	snap changelog.Snapshotter
	tail changelog.TailReader
}

// New returns a Reader over svc. It detects the backend's optional Snapshotter
// and TailReader capabilities by walking the Unwrap() chain from the Service
// down through its Log (the same discovery NewService uses); a Service that
// exposes no Unwrap simply gets full-replay reads.
func New(svc changelog.Service) *Reader {
	r := &Reader{svc: svc}
	var l changelog.Log
	if u, ok := svc.(interface{ Unwrap() changelog.Log }); ok {
		l = u.Unwrap()
	}
	for l != nil {
		if r.snap == nil {
			if s, ok := l.(changelog.Snapshotter); ok {
				r.snap = s
			}
		}
		if r.tail == nil {
			if tr, ok := l.(changelog.TailReader); ok {
				r.tail = tr
			}
		}
		if r.snap != nil && r.tail != nil {
			break
		}
		u, ok := l.(interface{ Unwrap() changelog.Log })
		if !ok {
			break
		}
		l = u.Unwrap()
	}
	return r
}

// Reconstruct replays commits (OLDEST first) into a document state, applying each
// change in order: put/create set the value at its path, delete removes it.
// Intermediate containers are created as needed (numeric segments make arrays).
// A change whose Kind is outside the kit vocabulary (create/put/delete) is an
// error — silently guessing would corrupt the reconstruction.
func Reconstruct(commits []changelog.Commit) (map[string]any, error) {
	cur, err := replayInto(map[string]any{}, commits)
	if err != nil {
		return nil, err
	}
	m, ok := cur.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("reconstruct: document root is not an object (got %T)", cur)
	}
	return m, nil
}

// replayInto applies commits (OLDEST first) on top of state, returning the
// (possibly rebound) root container.
func replayInto(state any, commits []changelog.Commit) (any, error) {
	cur := state
	for _, c := range commits {
		for _, ch := range c.Changes {
			next, err := docmodel.Apply(cur, ch)
			if err != nil {
				return nil, fmt.Errorf("reconstruct %s %q: %w", ch.Kind, ch.Path, err)
			}
			cur = next
		}
	}
	return cur, nil
}

// getAt navigates root to segs (dispatching on runtime type, so numeric object
// keys resolve correctly), returning the value and whether it was found.
func getAt(root map[string]any, segs []string) (any, bool) {
	var cur any = root
	for _, s := range segs {
		switch c := cur.(type) {
		case []any:
			idx, ok := docmodel.AsIndex(s)
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
	prefix := docmodel.SplitPath(paths[0])
	for _, p := range paths[1:] {
		segs := docmodel.SplitPath(p)
		j := 0
		for j < len(prefix) && j < len(segs) && prefix[j] == segs[j] {
			j++
		}
		prefix = prefix[:j]
		if len(prefix) == 0 {
			return ""
		}
	}
	return docmodel.JoinPath(prefix)
}

// State reconstructs docID's current state at HEAD. When the backend exposes
// both Snapshotter and TailReader (detected in New), it serves from the stored
// snapshot plus a tail replay instead of refetching the whole history, and
// refreshes the snapshot afterwards — turning reads on long histories from
// O(all commits) into O(commits since last read).
func (r *Reader) State(ctx context.Context, docID string) (map[string]any, error) {
	st, _, err := r.StateWithHead(ctx, docID)
	return st, err
}

// StateWithHead is State plus the ID of the commit the returned state was
// reconstructed at ("" for a document with no commits). A writer building on
// this state passes that ID as the commit's parent (changelog.WithParent) so
// the log records which snapshot the changes were actually diffed against.
func (r *Reader) StateWithHead(ctx context.Context, docID string) (map[string]any, string, error) {
	if r.snap != nil && r.tail != nil {
		if st, head, ok, err := r.snapshotState(ctx, docID); err != nil {
			return nil, "", err
		} else if ok {
			return st, head, nil
		}
	}
	commits, err := r.svc.Commits(ctx, docID, 0) // newest-first
	if err != nil {
		return nil, "", err
	}
	st, err := Reconstruct(reversed(commits))
	if err != nil {
		return nil, "", err
	}
	head := ""
	if len(commits) > 0 {
		head = commits[0].ID
	}
	if r.snap != nil && head != "" {
		r.saveSnapshot(ctx, docID, head, st) // prime the cache
	}
	return st, head, nil
}

// loadBase loads and decodes docID's stored snapshot. ok=false means "no
// usable snapshot" (none stored, or corrupt/foreign bytes) — the caller falls
// back to a full rebuild.
func (r *Reader) loadBase(ctx context.Context, docID string) (base map[string]any, commitID string, ok bool, err error) {
	s, ok, err := r.snap.LoadSnapshot(ctx, docID)
	if err != nil || !ok {
		return nil, "", false, err
	}
	if json.Unmarshal(s.State, &base) != nil || base == nil {
		return nil, "", false, nil
	}
	return base, s.CommitID, true, nil
}

// snapshotState serves State from the stored snapshot plus tail replay,
// returning the head commit ID the state was replayed to. ok=false means
// "fall back to a full rebuild" (no snapshot, undecodable bytes, an orphaned
// cursor, or a non-object root after replay) — never an error, because the
// full history can always answer.
func (r *Reader) snapshotState(ctx context.Context, docID string) (map[string]any, string, bool, error) {
	base, snapID, ok, err := r.loadBase(ctx, docID)
	if err != nil || !ok {
		return nil, "", false, err
	}
	tail, err := r.tail.CommitsAfter(ctx, docID, snapID, 0) // oldest-first
	if errors.Is(err, changelog.ErrNoSuchCommit) {
		return nil, "", false, nil // snapshot outlived its commit (doc reset): rebuild
	}
	if err != nil {
		return nil, "", false, err
	}
	if len(tail) == 0 {
		return base, snapID, true, nil
	}
	next, err := replayInto(base, tail)
	if err != nil {
		return nil, "", false, err
	}
	m, isObj := next.(map[string]any)
	if !isObj {
		return nil, "", false, nil
	}
	head := tail[len(tail)-1].ID
	// ponytail: snapshot refreshed on every read that replayed a tail; add a
	// commit-count threshold if the upsert traffic ever matters.
	r.saveSnapshot(ctx, docID, head, m)
	return m, head, true, nil
}

// saveSnapshot marshals and stores state as of commitID, best-effort (like
// Deduper.MarkSeen: a failure only means a colder next read, never an error).
func (r *Reader) saveSnapshot(ctx context.Context, docID, commitID string, state map[string]any) {
	b, err := json.Marshal(state)
	if err != nil {
		return
	}
	_ = r.snap.SaveSnapshot(ctx, changelog.Snapshot{DocID: docID, CommitID: commitID, State: b})
}

// StateAt reconstructs docID's state as of (and including) commitID. An empty
// commitID yields the empty document (the state before the root commit).
// When the backend exposes Snapshotter+TailReader and commitID is at or after
// the stored snapshot, it replays only the tail — O(commits since snapshot);
// older targets fall back to a full replay.
func (r *Reader) StateAt(ctx context.Context, docID, commitID string) (map[string]any, error) {
	if commitID != "" && r.snap != nil && r.tail != nil {
		if st, ok, err := r.snapshotStateAt(ctx, docID, commitID); err != nil {
			return nil, err
		} else if ok {
			return st, nil
		}
	}
	commits, err := r.svc.Commits(ctx, docID, 0)
	if err != nil {
		return nil, err
	}
	return stateUpTo(commits, commitID)
}

// snapshotStateAt serves StateAt from the stored snapshot plus a partial tail
// replay. ok=false means "fall back to a full rebuild" — no usable snapshot,
// an orphaned cursor, or commitID not in the tail (older than the snapshot, or
// not on the document at all; the full path tells those apart). It NEVER
// refreshes the snapshot: a historical read must not move the HEAD cache.
func (r *Reader) snapshotStateAt(ctx context.Context, docID, commitID string) (map[string]any, bool, error) {
	base, snapID, ok, err := r.loadBase(ctx, docID)
	if err != nil || !ok {
		return nil, false, err
	}
	if snapID == commitID {
		return base, true, nil
	}
	tail, err := r.tail.CommitsAfter(ctx, docID, snapID, 0) // oldest-first
	if errors.Is(err, changelog.ErrNoSuchCommit) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	for i := range tail {
		if tail[i].ID != commitID {
			continue
		}
		next, err := replayInto(base, tail[:i+1])
		if err != nil {
			return nil, false, err
		}
		m, isObj := next.(map[string]any)
		if !isObj {
			return nil, false, nil
		}
		return m, true, nil
	}
	return nil, false, nil
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
			return nil, fmt.Errorf("chronicleview: commit %q not found", commitID)
		}
	}
	return Reconstruct(upto)
}

// CommitSnapshot returns the per-commit LCA snapshot: the before-state (as of the
// commit's parent) of the smallest subtree containing every change in the commit.
// Scope = lcaPath of the commit's changed paths; if that scope is a scalar/leaf
// or missing in the parent state, it climbs to the enclosing container. When
// changes scatter (LCA = root) the snapshot is the whole prior document.
func (r *Reader) CommitSnapshot(ctx context.Context, docID, commitID string) (any, error) {
	if r.snap != nil && r.tail != nil {
		if v, ok, err := r.snapshotCommitScope(ctx, docID, commitID); err != nil {
			return nil, err
		} else if ok {
			return v, nil
		}
	}
	commits, err := r.svc.Commits(ctx, docID, 0)
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
		return nil, fmt.Errorf("chronicleview: commit %q not found in %q", commitID, docID)
	}
	before, err := stateUpTo(commits, target.Parent) // reuse the already-fetched list
	if err != nil {
		return nil, err
	}
	return lcaScope(before, target), nil
}

// snapshotCommitScope serves CommitSnapshot from the stored snapshot plus a
// partial tail replay: the before-state is the snapshot base advanced to the
// target's parent. ok=false falls back to the full path (no usable snapshot,
// orphaned cursor, or target at/before the snapshot — its before-state
// predates the base). Never refreshes the snapshot.
func (r *Reader) snapshotCommitScope(ctx context.Context, docID, commitID string) (any, bool, error) {
	base, snapID, ok, err := r.loadBase(ctx, docID)
	if err != nil || !ok {
		return nil, false, err
	}
	if snapID == commitID {
		return nil, false, nil
	}
	tail, err := r.tail.CommitsAfter(ctx, docID, snapID, 0) // oldest-first
	if errors.Is(err, changelog.ErrNoSuchCommit) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	for i := range tail {
		if tail[i].ID != commitID {
			continue
		}
		next, err := replayInto(base, tail[:i])
		if err != nil {
			return nil, false, err
		}
		before, isObj := next.(map[string]any)
		if !isObj {
			return nil, false, nil
		}
		return lcaScope(before, &tail[i]), true, nil
	}
	return nil, false, nil
}

// lcaScope returns the LCA-scoped subtree of before for target's changes: the
// value at the lcaPath of the changed paths, climbing to the nearest enclosing
// container when that scope is a scalar or absent, and the whole document when
// the scope reaches the root.
func lcaScope(before map[string]any, target *changelog.Commit) any {
	paths := make([]string, len(target.Changes))
	for i, c := range target.Changes {
		paths[i] = c.Path
	}
	scope := lcaPath(paths)
	val, ok := getAt(before, docmodel.SplitPath(scope))
	for scope != "" && (!ok || !docmodel.IsContainer(val)) {
		scope = docmodel.ParentPath(scope)
		val, ok = getAt(before, docmodel.SplitPath(scope))
	}
	if scope == "" {
		return before // root → whole prior document
	}
	return val
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
