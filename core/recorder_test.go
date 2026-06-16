package changelog

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func put(actor, path string) Change {
	return Change{Actor: actor, Path: path, Kind: "put", To: `"1"`}
}

func TestRecorder_AppendBuffers(t *testing.T) {
	r := NewRecorder("doc", newMemLog())
	r.Append(put("a", "x"))
	if len(r.Pending()) != 1 {
		t.Fatalf("want 1 pending, got %d", len(r.Pending()))
	}
}

func TestRecorder_AppendStampsClock(t *testing.T) {
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	r := NewRecorder("doc", newMemLog()).WithClock(func() time.Time { return at })
	r.Append(Change{Actor: "a", Path: "x", Kind: "put", At: time.Now()}) // caller's At must be overwritten
	got := r.Pending()[0].At
	if !got.Equal(at) {
		t.Fatalf("At = %v, want %v (recorder must overwrite caller's At)", got, at)
	}
}

func TestRecorder_CommitSealsAndChains(t *testing.T) {
	ctx := context.Background()
	log := newMemLog()
	r := NewRecorder("doc", log)

	r.Append(put("alice", "x"))
	r.Append(put("bob", "y"))
	c1, err := r.Commit(ctx)
	if err != nil {
		t.Fatalf("commit 1: %v", err)
	}
	if c1.Parent != "" {
		t.Fatalf("first commit parent = %q, want empty", c1.Parent)
	}
	if len(c1.Changes) != 2 {
		t.Fatalf("want 2 changes, got %d", len(c1.Changes))
	}
	if !reflect.DeepEqual(c1.Authors, []string{"alice", "bob"}) {
		t.Fatalf("authors = %v, want [alice bob]", c1.Authors)
	}
	if len(r.Pending()) != 0 {
		t.Fatal("pending not drained after commit")
	}

	r.Append(put("alice", "z"))
	c2, err := r.Commit(ctx)
	if err != nil {
		t.Fatalf("commit 2: %v", err)
	}
	if c2.Parent != c1.ID {
		t.Fatalf("c2.Parent = %q, want %q (chain broken)", c2.Parent, c1.ID)
	}

	stored, _ := log.Commits(ctx, "doc", 0)
	if len(stored) != 2 {
		t.Fatalf("log holds %d commits, want 2", len(stored))
	}
}

func TestRecorder_CommitEmptyErrors(t *testing.T) {
	_, err := NewRecorder("doc", newMemLog()).Commit(context.Background())
	if !errors.Is(err, ErrNothingToCommit) {
		t.Fatalf("want ErrNothingToCommit, got %v", err)
	}
}

func TestRecorder_WithMessageStoresAndHashes(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	fixed := func() time.Time { return at }

	plain := NewRecorder("doc", newMemLog()).WithClock(fixed)
	plain.Append(put("alice", "x"))
	pc, err := plain.Commit(ctx)
	if err != nil {
		t.Fatalf("plain commit: %v", err)
	}
	if pc.Message != "" {
		t.Fatalf("plain commit Message = %q, want empty", pc.Message)
	}

	annotated := NewRecorder("doc", newMemLog()).WithClock(fixed)
	annotated.Append(put("alice", "x"))
	ac, err := annotated.Commit(ctx, WithMessage("fix typo in name"))
	if err != nil {
		t.Fatalf("annotated commit: %v", err)
	}
	if ac.Message != "fix typo in name" {
		t.Fatalf("annotated Message = %q, want %q", ac.Message, "fix typo in name")
	}
	if ac.ID == pc.ID {
		t.Fatal("message must affect commit ID")
	}
}

func TestRecorder_NoOptionsMatchesPreOptionsID(t *testing.T) {
	// Calling Commit with no options must produce identical IDs to the
	// pre-options behavior. Two recorders, same fixed clock, same changes,
	// one calls Commit(ctx), the other Commit(ctx) with no opts — IDs match.
	ctx := context.Background()
	at := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	fixed := func() time.Time { return at }

	r1 := NewRecorder("doc", newMemLog()).WithClock(fixed)
	r1.Append(put("alice", "x"))
	c1, _ := r1.Commit(ctx)

	r2 := NewRecorder("doc", newMemLog()).WithClock(fixed)
	r2.Append(put("alice", "x"))
	c2, _ := r2.Commit(ctx, []CommitOption{}...)

	if c1.ID != c2.ID {
		t.Fatalf("no-option call drifted: %s vs %s", c1.ID, c2.ID)
	}
}

func TestRecorder_FixedClockYieldsDeterministicCommitID(t *testing.T) {
	at := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	fixed := func() time.Time { return at }

	build := func() *Recorder {
		return NewRecorder("doc", newMemLog()).WithClock(fixed)
	}
	run := func(r *Recorder) string {
		r.Append(put("alice", "x"))
		r.Append(put("bob", "y"))
		c, err := r.Commit(context.Background())
		if err != nil {
			t.Fatalf("commit: %v", err)
		}
		return c.ID
	}

	id1 := run(build())
	id2 := run(build())
	if id1 != id2 {
		t.Fatalf("non-deterministic commit IDs: %s vs %s", id1, id2)
	}
}
