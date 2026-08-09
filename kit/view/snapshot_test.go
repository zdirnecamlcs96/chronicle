package chronicleview

import (
	"context"
	"reflect"
	"testing"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
	"github.com/zdirnecamlcs96/chronicle/kit/internal/memlog"
	chronicleschema "github.com/zdirnecamlcs96/chronicle/kit/schema"
)

func put(path, to string) changelog.Change {
	return changelog.Change{Actor: "a", Path: path, Kind: chronicleschema.KindPut, To: to}
}

// opaqueService hides the concrete service's Unwrap so the Reader sees no
// capabilities.
type opaqueService struct{ changelog.Service }

// seal is the write side these read tests need; the kit's facade lives a layer
// up and would import this package.
func seal(t *testing.T, svc changelog.Service, docID string, changes ...changelog.Change) changelog.Commit {
	t.Helper()
	c, err := svc.Seal(context.Background(), docID, changes, "")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestState_SnapshotFastPath(t *testing.T) {
	ctx := context.Background()
	log := memlog.New()
	svc := changelog.NewService(log)
	r := New(svc)

	seal(t, svc, "doc", put("a", "1"), put("b", "2"))
	// First read: no snapshot yet → one full Commits fetch, then primes the cache.
	s1, err := r.State(ctx, "doc")
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := log.Counters(); c != 1 {
		t.Fatalf("first State: commitsCalls=%d, want 1", c)
	}
	if len(log.Snapshots()) != 1 {
		t.Fatalf("first State did not prime the snapshot: %d stored", len(log.Snapshots()))
	}

	// Second read after one more commit: must use the cursor, not full history.
	seal(t, svc, "doc", put("b", "3"))
	s2, err := r.State(ctx, "doc")
	if err != nil {
		t.Fatal(err)
	}
	commitsCalls, afterCalls := log.Counters()
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

func TestStateWithHead(t *testing.T) {
	ctx := context.Background()
	log := memlog.New()
	svc := changelog.NewService(log)
	r := New(svc)

	// Empty document: no commits, empty head.
	if _, head, err := r.StateWithHead(ctx, "doc"); err != nil || head != "" {
		t.Fatalf("empty doc: head=%q err=%v, want \"\"/nil", head, err)
	}

	c1 := seal(t, svc, "doc", put("a", "1"))
	// Full-replay path (first read, no snapshot yet).
	if _, head, err := r.StateWithHead(ctx, "doc"); err != nil || head != c1.ID {
		t.Fatalf("full path: head=%q err=%v, want %q", head, err, c1.ID)
	}
	// Fast path, caught up: head is the snapshot's own commit.
	if _, head, err := r.StateWithHead(ctx, "doc"); err != nil || head != c1.ID {
		t.Fatalf("caught-up fast path: head=%q err=%v, want %q", head, err, c1.ID)
	}
	// Fast path with a tail replay: head is the last tail commit.
	c2 := seal(t, svc, "doc", put("a", "2"))
	if _, head, err := r.StateWithHead(ctx, "doc"); err != nil || head != c2.ID {
		t.Fatalf("tail fast path: head=%q err=%v, want %q", head, err, c2.ID)
	}
	// Capability-blind reader: full replay must report the same head.
	blind := New(opaqueService{svc})
	if _, head, err := blind.StateWithHead(ctx, "doc"); err != nil || head != c2.ID {
		t.Fatalf("blind full path: head=%q err=%v, want %q", head, err, c2.ID)
	}
}

func TestStateAt_SnapshotFastPath(t *testing.T) {
	ctx := context.Background()
	log := memlog.New()
	svc := changelog.NewService(log)
	r := New(svc)

	c1 := seal(t, svc, "doc", put("a", "1"))
	// Prime the snapshot at c1 (HEAD), then grow the history past it.
	if _, err := r.State(ctx, "doc"); err != nil {
		t.Fatal(err)
	}
	c2 := seal(t, svc, "doc", put("b", "2"))
	seal(t, svc, "doc", put("b", "3"))

	// Target after the snapshot: must ride the cursor, not full history.
	got, err := r.StateAt(ctx, "doc", c2.ID)
	if err != nil {
		t.Fatal(err)
	}
	commitsCalls, afterCalls := log.Counters()
	if commitsCalls != 1 || afterCalls == 0 {
		t.Fatalf("StateAt: commitsCalls=%d afterCalls=%d, want 1/>0", commitsCalls, afterCalls)
	}
	// Historical read must not move the HEAD snapshot cache backwards.
	if log.Snapshots()["doc"].CommitID != c1.ID {
		t.Fatalf("StateAt moved the snapshot to %q, want untouched %q", log.Snapshots()["doc"].CommitID, c1.ID)
	}

	// Target AT the snapshot commit: served straight from the decoded base.
	atSnap, err := r.StateAt(ctx, "doc", c1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := log.Counters(); c != 1 {
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
	log := memlog.New()
	svc := changelog.NewService(log)
	r := New(svc)

	c1 := seal(t, svc, "doc", put("a", "1"))
	seal(t, svc, "doc", put("a", "2"))
	if _, err := r.State(ctx, "doc"); err != nil { // snapshot at HEAD (c2)
		t.Fatal(err)
	}
	// c1 predates the snapshot: the probe misses and the full path answers.
	st, err := r.StateAt(ctx, "doc", c1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st["a"] != float64(1) {
		t.Fatalf("state = %#v, want a=1", st)
	}
	// Unknown commits still error even when a snapshot exists.
	if _, err := r.StateAt(ctx, "doc", "nonexistent"); err == nil {
		t.Fatal("unknown commit id must error, not return HEAD state")
	}
}

func TestCommitSnapshot_SnapshotFastPath(t *testing.T) {
	ctx := context.Background()
	log := memlog.New()
	svc := changelog.NewService(log)
	r := New(svc)

	seal(t, svc, "doc", put("items.k1.qty", "1"), put("items.k1.price", "2"), put("status", `"open"`))
	if _, err := r.State(ctx, "doc"); err != nil { // prime snapshot at c1
		t.Fatal(err)
	}
	c2 := seal(t, svc, "doc", put("items.k1.qty", "5"), put("items.k1.price", "9"))

	got, err := r.CommitSnapshot(ctx, "doc", c2.ID)
	if err != nil {
		t.Fatal(err)
	}
	commitsCalls, afterCalls := log.Counters()
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
	log := memlog.New()
	svc := changelog.NewService(log)
	r := New(svc)
	seal(t, svc, "doc", put("a", "1"))
	if err := log.SaveSnapshot(ctx, changelog.Snapshot{DocID: "doc", CommitID: "whatever", State: []byte("{not json")}); err != nil {
		t.Fatal(err)
	}
	st, err := r.State(ctx, "doc")
	if err != nil {
		t.Fatalf("corrupt snapshot must fall back, got %v", err)
	}
	if st["a"] != float64(1) {
		t.Fatalf("state = %#v", st)
	}
}

func TestState_OrphanedCursorRebuilds(t *testing.T) {
	ctx := context.Background()
	log := memlog.New()
	svc := changelog.NewService(log)
	r := New(svc)
	seal(t, svc, "doc", put("a", "1"))
	if err := log.SaveSnapshot(ctx, changelog.Snapshot{DocID: "doc", CommitID: "gone", State: []byte(`{"stale":true}`)}); err != nil {
		t.Fatal(err)
	}
	st, err := r.State(ctx, "doc")
	if err != nil {
		t.Fatalf("orphaned cursor must fall back, got %v", err)
	}
	if _, stale := st["stale"]; stale || st["a"] != float64(1) {
		t.Fatalf("state = %#v", st)
	}
}

func TestState_NoCapabilitiesStillWorks(t *testing.T) {
	ctx := context.Background()
	svc := opaqueService{memlog.NewService()}
	r := New(svc)
	if r.snap != nil || r.tail != nil {
		t.Fatal("opaque service must yield no capabilities")
	}
	seal(t, svc, "doc", put("a", "1"))
	st, err := r.State(ctx, "doc")
	if err != nil || st["a"] != float64(1) {
		t.Fatalf("state=%#v err=%v", st, err)
	}
}
