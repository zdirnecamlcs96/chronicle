package chroniclekit

import (
	"context"
	"reflect"
	"strings"
	"testing"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
)

func put(path, to string) changelog.Change {
	return changelog.Change{Actor: "a", Path: path, Kind: KindPut, To: to}
}

// opaqueService hides the concrete service's Unwrap so the Kit sees no
// capabilities.
type opaqueService struct{ changelog.Service }

func TestState_SnapshotFastPath(t *testing.T) {
	ctx := context.Background()
	log := newMemLog()
	k := New(changelog.NewService(log))

	if _, err := k.RecordChanges(ctx, "doc", []changelog.Change{put("a", "1"), put("b", "2")}); err != nil {
		t.Fatal(err)
	}
	// First read: no snapshot yet → one full Commits fetch, then primes the cache.
	s1, err := k.State(ctx, "doc")
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := log.counters(); c != 1 {
		t.Fatalf("first State: commitsCalls=%d, want 1", c)
	}
	if len(log.snaps) != 1 {
		t.Fatalf("first State did not prime the snapshot: %d stored", len(log.snaps))
	}

	// Second read after one more commit: must use the cursor, not full history.
	if _, err := k.RecordChanges(ctx, "doc", []changelog.Change{put("b", "3")}); err != nil {
		t.Fatal(err)
	}
	s2, err := k.State(ctx, "doc")
	if err != nil {
		t.Fatal(err)
	}
	commitsCalls, afterCalls := log.counters()
	if commitsCalls != 1 || afterCalls == 0 {
		t.Fatalf("second State: commitsCalls=%d afterCalls=%d, want 1/>0", commitsCalls, afterCalls)
	}

	// Result must be identical to a capability-blind full replay.
	blind := New(opaqueService{changelog.NewService(log)})
	want, err := blind.State(ctx, "doc")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s2, want) {
		t.Fatalf("fast path state %#v != full replay %#v", s2, want)
	}
	if reflect.DeepEqual(s1, s2) {
		t.Fatal("second commit did not change the state under test")
	}
}

func TestStateAt_SnapshotFastPath(t *testing.T) {
	ctx := context.Background()
	log := newMemLog()
	k := New(changelog.NewService(log))

	c1, err := k.RecordChanges(ctx, "doc", []changelog.Change{put("a", "1")})
	if err != nil {
		t.Fatal(err)
	}
	// Prime the snapshot at c1 (HEAD), then grow the history past it.
	if _, err := k.State(ctx, "doc"); err != nil {
		t.Fatal(err)
	}
	c2, err := k.RecordChanges(ctx, "doc", []changelog.Change{put("b", "2")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.RecordChanges(ctx, "doc", []changelog.Change{put("b", "3")}); err != nil {
		t.Fatal(err)
	}

	// Target after the snapshot: must ride the cursor, not full history.
	got, err := k.StateAt(ctx, "doc", c2.ID)
	if err != nil {
		t.Fatal(err)
	}
	commitsCalls, afterCalls := log.counters()
	if commitsCalls != 1 || afterCalls == 0 {
		t.Fatalf("StateAt: commitsCalls=%d afterCalls=%d, want 1/>0", commitsCalls, afterCalls)
	}
	// Historical read must not move the HEAD snapshot cache backwards.
	if log.snaps["doc"].CommitID != c1.ID {
		t.Fatalf("StateAt moved the snapshot to %q, want untouched %q", log.snaps["doc"].CommitID, c1.ID)
	}

	// Target AT the snapshot commit: served straight from the decoded base.
	atSnap, err := k.StateAt(ctx, "doc", c1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := log.counters(); c != 1 {
		t.Fatalf("StateAt(snapshot commit): commitsCalls=%d, want 1", c)
	}

	// Both must be byte-identical to a capability-blind full replay.
	blind := New(opaqueService{changelog.NewService(log)})
	want2, err := blind.StateAt(ctx, "doc", c2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want2) {
		t.Fatalf("fast path %#v != full replay %#v", got, want2)
	}
	want1, err := blind.StateAt(ctx, "doc", c1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(atSnap, want1) {
		t.Fatalf("fast path at snapshot %#v != full replay %#v", atSnap, want1)
	}
}

func TestStateAt_TargetBeforeSnapshotFallsBack(t *testing.T) {
	ctx := context.Background()
	log := newMemLog()
	k := New(changelog.NewService(log))

	c1, err := k.RecordChanges(ctx, "doc", []changelog.Change{put("a", "1")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.RecordChanges(ctx, "doc", []changelog.Change{put("a", "2")}); err != nil {
		t.Fatal(err)
	}
	if _, err := k.State(ctx, "doc"); err != nil { // snapshot at HEAD (c2)
		t.Fatal(err)
	}
	// c1 predates the snapshot: the probe misses and the full path answers.
	st, err := k.StateAt(ctx, "doc", c1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st["a"] != float64(1) {
		t.Fatalf("state = %#v, want a=1", st)
	}
	// Unknown commits still error even when a snapshot exists.
	if _, err := k.StateAt(ctx, "doc", "nonexistent"); err == nil {
		t.Fatal("unknown commit id must error, not return HEAD state")
	}
}

func TestCommitSnapshot_SnapshotFastPath(t *testing.T) {
	ctx := context.Background()
	log := newMemLog()
	k := New(changelog.NewService(log))

	if _, err := k.RecordChanges(ctx, "doc", []changelog.Change{
		put("items.k1.qty", "1"), put("items.k1.price", "2"), put("status", `"open"`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := k.State(ctx, "doc"); err != nil { // prime snapshot at c1
		t.Fatal(err)
	}
	c2, err := k.RecordChanges(ctx, "doc", []changelog.Change{
		put("items.k1.qty", "5"), put("items.k1.price", "9"),
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := k.CommitSnapshot(ctx, "doc", c2.ID)
	if err != nil {
		t.Fatal(err)
	}
	commitsCalls, afterCalls := log.counters()
	if commitsCalls != 1 || afterCalls == 0 {
		t.Fatalf("CommitSnapshot: commitsCalls=%d afterCalls=%d, want 1/>0", commitsCalls, afterCalls)
	}
	blind := New(opaqueService{changelog.NewService(log)})
	want, err := blind.CommitSnapshot(ctx, "doc", c2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fast path %#v != full replay %#v", got, want)
	}
}

func TestState_CorruptSnapshotSelfHeals(t *testing.T) {
	ctx := context.Background()
	log := newMemLog()
	k := New(changelog.NewService(log))
	if _, err := k.RecordChanges(ctx, "doc", []changelog.Change{put("a", "1")}); err != nil {
		t.Fatal(err)
	}
	log.snaps["doc"] = changelog.Snapshot{DocID: "doc", CommitID: "whatever", State: []byte("{not json")}
	st, err := k.State(ctx, "doc")
	if err != nil {
		t.Fatalf("corrupt snapshot must fall back, got %v", err)
	}
	if st["a"] != float64(1) {
		t.Fatalf("state = %#v", st)
	}
}

func TestState_OrphanedCursorRebuilds(t *testing.T) {
	ctx := context.Background()
	log := newMemLog()
	k := New(changelog.NewService(log))
	if _, err := k.RecordChanges(ctx, "doc", []changelog.Change{put("a", "1")}); err != nil {
		t.Fatal(err)
	}
	log.snaps["doc"] = changelog.Snapshot{DocID: "doc", CommitID: "gone", State: []byte(`{"stale":true}`)}
	st, err := k.State(ctx, "doc")
	if err != nil {
		t.Fatalf("orphaned cursor must fall back, got %v", err)
	}
	if _, stale := st["stale"]; stale || st["a"] != float64(1) {
		t.Fatalf("state = %#v", st)
	}
}

func TestState_NoCapabilitiesStillWorks(t *testing.T) {
	ctx := context.Background()
	k := New(opaqueService{newMemService()})
	if k.snap != nil || k.tail != nil {
		t.Fatal("opaque service must yield no capabilities")
	}
	if _, err := k.RecordChanges(ctx, "doc", []changelog.Change{put("a", "1")}); err != nil {
		t.Fatal(err)
	}
	st, err := k.State(ctx, "doc")
	if err != nil || st["a"] != float64(1) {
		t.Fatalf("state=%#v err=%v", st, err)
	}
}

func TestReconstruct_UnknownKindErrors(t *testing.T) {
	ctx := context.Background()
	k := New(newMemService())
	if _, err := k.RecordChanges(ctx, "doc", []changelog.Change{{Actor: "a", Path: "p", Kind: "merge", To: "1"}}); err != nil {
		t.Fatal(err)
	}
	_, err := k.State(ctx, "doc")
	if err == nil || !strings.Contains(err.Error(), `unknown change kind "merge"`) {
		t.Fatalf("err = %v, want unknown-kind error", err)
	}
}

func TestDottedKeyEndToEnd(t *testing.T) {
	// The actual bug being fixed: an object key containing "." must survive
	// Diff → seal → State.
	ctx := context.Background()
	k := New(newMemService())
	after := map[string]any{"a.b": 1, "c": map[string]any{"d.e": "x"}}
	if _, err := k.RecordUpdate(ctx, "doc", map[string]any{}, after); err != nil {
		t.Fatal(err)
	}
	st, err := k.State(ctx, "doc")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(st, norm(t, after)) {
		t.Fatalf("state = %#v, want %#v", st, norm(t, after))
	}
}
