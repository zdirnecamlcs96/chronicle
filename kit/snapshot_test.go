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
