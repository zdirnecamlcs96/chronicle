package chroniclekit

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
	"github.com/zdirnecamlcs96/chronicle/kit/internal/memlog"
)

// New takes a Log, so a caller picks a backend and nothing else — the Service
// is built for them. Round-trip a write and a read to prove the Kit built this
// way is fully wired, capability detection included.
func TestNew_FromLogAlone(t *testing.T) {
	ctx := context.Background()
	k := New(memlog.New())

	if _, err := k.RecordUpdate(ctx, "doc", nil, map[string]any{"status": "open"}); err != nil {
		t.Fatal(err)
	}
	state, err := k.State(ctx, "doc")
	if err != nil {
		t.Fatal(err)
	}
	if state["status"] != "open" {
		t.Fatalf("state = %v, want status open", state)
	}
}

func TestWithActor(t *testing.T) {
	ctx := context.Background()
	k := New(memlog.New())

	c, err := k.RecordUpdate(ctx, "doc", nil, map[string]any{"status": "open", "qty": 1},
		WithActor("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Authors, []string{"alice"}) {
		t.Fatalf("authors = %v, want [alice]", c.Authors)
	}
	for _, ch := range c.Changes {
		if ch.Actor != "alice" {
			t.Fatalf("change %s actor = %q, want alice", ch.Path, ch.Actor)
		}
	}

	// An actor already on a change wins — WithActor fills blanks, it does not
	// rewrite attribution someone set deliberately.
	mixed := []changelog.Change{
		{Path: "a", Kind: "put", To: "1", Actor: "bob"},
		{Path: "b", Kind: "put", To: "2"},
	}
	c2, err := k.RecordChanges(ctx, "doc2", mixed, WithActor("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c2.Authors, []string{"alice", "bob"}) {
		t.Fatalf("authors = %v, want [alice bob]", c2.Authors)
	}
}

func TestKit_RecordUpdate_SealsDiff(t *testing.T) {
	ctx := context.Background()
	k := NewWithService(memlog.NewService())

	before := map[string]any{"status": "draft", "qty": 1}
	after := map[string]any{"status": "sent", "qty": 1}
	c, err := k.RecordUpdate(ctx, "doc", before, after)
	if err != nil {
		t.Fatalf("RecordUpdate: %v", err)
	}
	if len(c.Changes) != 1 || c.Changes[0].Path != "status" || c.Changes[0].To != `"sent"` {
		t.Fatalf("want one status change, got %+v", c.Changes)
	}
}

func TestKit_RecordUpdate_EmptyDiffErrors(t *testing.T) {
	ctx := context.Background()
	k := NewWithService(memlog.NewService())
	doc := map[string]any{"a": 1}
	_, err := k.RecordUpdate(ctx, "doc", doc, doc)
	if !errors.Is(err, changelog.ErrEmptyChanges) {
		t.Fatalf("want ErrEmptyChanges for no-op update, got %v", err)
	}
}

func TestKit_Idempotency(t *testing.T) {
	ctx := context.Background()
	k := NewWithService(memlog.NewService())
	changes := []changelog.Change{{Actor: "a", Path: "x", Kind: KindPut, To: "1"}}
	c1, err := k.RecordChanges(ctx, "doc", changes, WithIdempotencyKey("k1"))
	if err != nil {
		t.Fatal(err)
	}
	c2, err := k.RecordChanges(ctx, "doc", changes, WithIdempotencyKey("k1"))
	if err != nil {
		t.Fatal(err)
	}
	if c1.ID != c2.ID {
		t.Fatalf("idempotent replay must return same commit: %s vs %s", c1.ID, c2.ID)
	}
}

func TestKit_State_AfterUpdates(t *testing.T) {
	ctx := context.Background()
	k := NewWithService(memlog.NewService())
	doc0 := map[string]any{"name": "a", "qty": 1}
	doc1 := map[string]any{"name": "a", "qty": 5}
	if _, err := k.RecordUpdate(ctx, "doc", nil, doc0); err != nil {
		t.Fatal(err)
	}
	if _, err := k.RecordUpdate(ctx, "doc", doc0, doc1); err != nil {
		t.Fatal(err)
	}
	got, err := k.State(ctx, "doc")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, norm(t, doc1)) {
		t.Fatalf("state mismatch:\n got  %#v\n want %#v", got, norm(t, doc1))
	}
}

func TestKit_StateAt_StopsAtCommit(t *testing.T) {
	ctx := context.Background()
	k := NewWithService(memlog.NewService())
	doc0 := map[string]any{"qty": 1}
	doc1 := map[string]any{"qty": 2}
	c0, _ := k.RecordUpdate(ctx, "doc", nil, doc0)
	k.RecordUpdate(ctx, "doc", doc0, doc1)

	at0, err := k.StateAt(ctx, "doc", c0.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(at0, norm(t, doc0)) {
		t.Fatalf("StateAt(c0) = %#v, want %#v", at0, norm(t, doc0))
	}
}

func TestKit_StateAt_UnknownCommitErrors(t *testing.T) {
	ctx := context.Background()
	k := NewWithService(memlog.NewService())
	if _, err := k.RecordUpdate(ctx, "doc", nil, map[string]any{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := k.StateAt(ctx, "doc", "nonexistent"); err == nil {
		t.Fatal("StateAt with an unknown commit id must error, not return HEAD state")
	}
}

func TestKit_CommitSnapshot_LCA(t *testing.T) {
	ctx := context.Background()
	k := NewWithService(memlog.NewService())

	doc0 := map[string]any{
		"items":  []any{map[string]any{"qty": 1, "price": 10}, map[string]any{"qty": 2, "price": 20}},
		"status": "draft",
	}
	if _, err := k.RecordUpdate(ctx, "doc", nil, doc0); err != nil {
		t.Fatal(err)
	}

	// Clustered: both changes inside items[0] → LCA items.0 → snapshot items[0]
	// before-state, NOT items[1].
	doc1 := map[string]any{
		"items":  []any{map[string]any{"qty": 5, "price": 15}, map[string]any{"qty": 2, "price": 20}},
		"status": "draft",
	}
	c1, err := k.RecordUpdate(ctx, "doc", doc0, doc1)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := k.CommitSnapshot(ctx, "doc", c1.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantSnap := norm(t, map[string]any{"qty": 1, "price": 10}) // items[0] BEFORE this commit
	if !reflect.DeepEqual(snap, wantSnap) {
		t.Fatalf("clustered snapshot = %#v, want items[0] before-state %#v", snap, wantSnap)
	}

	// Scattered: items[0].qty + status → LCA "" → snapshot the whole prior doc.
	doc2 := map[string]any{
		"items":  []any{map[string]any{"qty": 9, "price": 15}, map[string]any{"qty": 2, "price": 20}},
		"status": "sent",
	}
	c2, err := k.RecordUpdate(ctx, "doc", doc1, doc2)
	if err != nil {
		t.Fatal(err)
	}
	snap2, err := k.CommitSnapshot(ctx, "doc", c2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snap2, norm(t, doc1)) { // whole doc as of parent (doc1)
		t.Fatalf("scattered snapshot must be whole prior doc:\n got  %#v\n want %#v", snap2, norm(t, doc1))
	}
}

func TestKit_CommitSnapshot_SingleChangeClimbsToParent(t *testing.T) {
	ctx := context.Background()
	k := NewWithService(memlog.NewService())
	doc0 := map[string]any{"items": []any{map[string]any{"qty": 1, "price": 10}}}
	k.RecordUpdate(ctx, "doc", nil, doc0)
	doc1 := map[string]any{"items": []any{map[string]any{"qty": 7, "price": 10}}}
	c1, _ := k.RecordUpdate(ctx, "doc", doc0, doc1)

	snap, err := k.CommitSnapshot(ctx, "doc", c1.ID)
	if err != nil {
		t.Fatal(err)
	}
	// LCA of the single path items.0.qty is the leaf itself; CommitSnapshot must
	// climb to the enclosing object items[0].
	want := norm(t, map[string]any{"qty": 1, "price": 10})
	if !reflect.DeepEqual(snap, want) {
		t.Fatalf("single-change snapshot = %#v, want enclosing object %#v", snap, want)
	}
}

// raceLog wraps a Log and runs fn immediately before the FIRST AppendCommit
// passes through — a deterministic stand-in for a writer landing between
// RecordPatch's state read and its append.
type raceLog struct {
	changelog.Log
	fn    func()
	fired bool
}

func (r *raceLog) AppendCommit(ctx context.Context, docID string, c changelog.Commit) error {
	if !r.fired {
		r.fired = true
		r.fn()
	}
	return r.Log.AppendCommit(ctx, docID, c)
}

// The headline fork-tolerance test: a writer landing between RecordPatch's
// state read and its seal must NOT be silently rebased over. The patch commit
// records the head its state was read at as parent (a fork), its From values
// match that snapshot, the racer's commit survives, and the folded state is
// arrival-order last-write-wins.
func TestKit_RecordPatch_ConcurrentWriterRecordsFork(t *testing.T) {
	ctx := context.Background()
	inner := memlog.New()
	setup := New(inner)

	base, err := setup.RecordUpdate(ctx, "T1", nil, map[string]any{"status": "draft"}, WithActor("init"))
	if err != nil {
		t.Fatal(err)
	}

	var racer changelog.Commit
	wrapped := &raceLog{Log: inner}
	wrapped.fn = func() {
		racer, err = setup.RecordUpdate(ctx, "T1",
			map[string]any{"status": "draft"},
			map[string]any{"status": "closed"}, WithActor("bob"))
		if err != nil {
			t.Fatalf("racer: %v", err)
		}
	}

	k := New(wrapped)
	patch, err := k.RecordPatch(ctx, "T1", []Operation{
		{Op: "replace", Path: "/status", Value: json.RawMessage(`"open"`)},
	}, WithActor("alice"))
	if err != nil {
		t.Fatalf("RecordPatch: %v", err)
	}

	if patch.Parent != base.ID {
		t.Fatalf("patch.Parent = %q, want the snapshot it was diffed against %q (not the racer %q)",
			patch.Parent, base.ID, racer.ID)
	}
	if racer.Parent != base.ID {
		t.Fatalf("racer.Parent = %q, want %q — test setup must produce a fork", racer.Parent, base.ID)
	}
	if len(patch.Changes) != 1 {
		t.Fatalf("patch changes = %d, want 1", len(patch.Changes))
	}
	ch := patch.Changes[0]
	if ch.Path != "status" || ch.From != `"draft"` || ch.To != `"open"` {
		t.Fatalf("change = %+v, want status \"draft\"→\"open\" (From from the READ snapshot, never the racer's state)", ch)
	}

	stored, err := inner.Commits(ctx, "T1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 3 {
		t.Fatalf("stored %d commits, want 3 — the racer must survive", len(stored))
	}
	if err := changelog.VerifyChain(stored); err != nil {
		t.Fatalf("forked history must verify: %v", err)
	}

	// Arrival-order last-write-wins: racer landed before the patch, patch wins.
	state, err := k.State(ctx, "T1")
	if err != nil {
		t.Fatal(err)
	}
	if state["status"] != "open" {
		t.Fatalf("folded state = %v, want status open (arrival LWW)", state)
	}
}
