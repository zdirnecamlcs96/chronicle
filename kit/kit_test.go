package chroniclekit

import (
	"context"
	"errors"
	"reflect"
	"testing"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
)

func TestKit_RecordUpdate_SealsDiff(t *testing.T) {
	ctx := context.Background()
	k := New(newMemService())

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
	k := New(newMemService())
	doc := map[string]any{"a": 1}
	_, err := k.RecordUpdate(ctx, "doc", doc, doc)
	if !errors.Is(err, changelog.ErrEmptyChanges) {
		t.Fatalf("want ErrEmptyChanges for no-op update, got %v", err)
	}
}

func TestKit_Idempotency(t *testing.T) {
	ctx := context.Background()
	k := New(newMemService())
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
	k := New(newMemService())
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
	k := New(newMemService())
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
	k := New(newMemService())
	if _, err := k.RecordUpdate(ctx, "doc", nil, map[string]any{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := k.StateAt(ctx, "doc", "nonexistent"); err == nil {
		t.Fatal("StateAt with an unknown commit id must error, not return HEAD state")
	}
}

func TestKit_CommitSnapshot_LCA(t *testing.T) {
	ctx := context.Background()
	k := New(newMemService())

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
	k := New(newMemService())
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
