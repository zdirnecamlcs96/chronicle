package changelogmemory_test

import (
	"context"
	"testing"

	changelogmemory "github.com/zdirnecamlcs96/chronicle/adapters/memory"
	"github.com/zdirnecamlcs96/chronicle/core"
	"github.com/zdirnecamlcs96/chronicle/core/conformance"
)

// The memory adapter must satisfy the mandatory Log contract, ForkAppend
// included: AppendCommit stores whatever parent the writer asserted, so two
// commits sharing a parent are a recorded fork, not an error.
func TestMemoryLog_Conformance(t *testing.T) {
	conformance.RunLogConformance(t, func(t *testing.T) (changelog.Log, func()) {
		return changelogmemory.New(), func() {}
	})
}

// The memory adapter implements changelog.Deduper, so it must honor the
// per-document idempotency-key scoping contract: a key marked on one document
// must not resolve on another.
func TestMemoryLog_DeduperConformance(t *testing.T) {
	conformance.RunDeduperConformance(t, func(t *testing.T) (changelog.Log, func()) {
		return changelogmemory.New(), func() {}
	})
}

// The memory adapter implements changelog.TailReader (cursor reads of a
// document's chain, oldest first).
func TestMemoryLog_TailReaderConformance(t *testing.T) {
	conformance.RunTailReaderConformance(t, func(t *testing.T) (changelog.Log, func()) {
		return changelogmemory.New(), func() {}
	})
}

// The memory adapter implements changelog.Snapshotter (one cached snapshot per
// document, latest wins).
func TestMemoryLog_SnapshotterConformance(t *testing.T) {
	conformance.RunSnapshotterConformance(t, func(t *testing.T) (changelog.Log, func()) {
		return changelogmemory.New(), func() {}
	})
}

// The memory adapter implements changelog.Annotator (one display sidecar per
// commit, outside the hash seal, latest wins).
func TestMemoryLog_AnnotatorConformance(t *testing.T) {
	conformance.RunAnnotatorConformance(t, func(t *testing.T) (changelog.Log, func()) {
		return changelogmemory.New(), func() {}
	})
}

// The memory adapter implements changelog.Indexer (cross-document queries),
// moved here from the core service so the service holds no storage state.
func TestMemoryLog_Indexer(t *testing.T) {
	ctx := context.Background()
	log := changelogmemory.New()

	seal := func(docID, to string) changelog.Commit {
		rec := changelog.NewRecorder(docID, log)
		rec.Append(changelog.Change{Actor: "a", Path: "p", Kind: "put", To: to})
		c, err := rec.Commit(ctx)
		if err != nil {
			t.Fatalf("seal %s: %v", docID, err)
		}
		return c
	}
	a := seal("docA", "1")
	b := seal("docB", "2")

	all, err := log.AllCommits(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("AllCommits = %d, want 2 (one per document)", len(all))
	}

	if got, ok, _ := log.FindByID(ctx, a.ID); !ok || got.DocID != "docA" || got.Commit.ID != a.ID {
		t.Fatalf("FindByID(a) = %+v ok=%v, want docA/%s", got, ok, a.ID)
	}
	if got, ok, _ := log.FindByID(ctx, b.ID); !ok || got.DocID != "docB" || got.Commit.ID != b.ID {
		t.Fatalf("FindByID(b) = %+v ok=%v, want docB/%s", got, ok, b.ID)
	}
	if _, ok, _ := log.FindByID(ctx, "deadbeef"); ok {
		t.Fatal("FindByID(missing) returned ok=true")
	}
}

// The memory adapter implements Tips (not yet a core.Tipper the pinned core
// release declares — called directly on the concrete type, see
// memorylog.go). A linear chain has one tip equal to Head; a fork has one per
// branch, chronological.
func TestMemoryLog_Tips(t *testing.T) {
	ctx := context.Background()
	log := changelogmemory.New()

	if got, err := log.Tips(ctx, "missing"); err != nil || len(got) != 0 {
		t.Fatalf("unknown doc: got %v err=%v, want empty/nil", got, err)
	}

	rec := changelog.NewRecorder("doc", log)
	rec.Append(changelog.Change{Actor: "a", Path: "p", Kind: "put", To: "1"})
	if _, err := rec.Commit(ctx); err != nil {
		t.Fatalf("root: %v", err)
	}
	rec.Append(changelog.Change{Actor: "a", Path: "p", Kind: "put", To: "2"})
	mid, err := rec.Commit(ctx)
	if err != nil {
		t.Fatalf("mid: %v", err)
	}
	if got, err := log.Tips(ctx, "doc"); err != nil || len(got) != 1 || got[0] != mid.ID {
		t.Fatalf("linear tips = %v err=%v, want [%q]", got, err, mid.ID)
	}

	rec.Append(changelog.Change{Actor: "a", Path: "p", Kind: "put", To: "3a"})
	a, err := rec.Commit(ctx, changelog.WithParent(mid.ID))
	if err != nil {
		t.Fatalf("childA: %v", err)
	}
	rec.Append(changelog.Change{Actor: "a", Path: "p", Kind: "put", To: "3b"})
	b, err := rec.Commit(ctx, changelog.WithParent(mid.ID))
	if err != nil {
		t.Fatalf("childB: %v", err)
	}
	got, err := log.Tips(ctx, "doc")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != a.ID || got[1] != b.ID {
		t.Fatalf("fork tips = %v, want [%q %q] (chronological)", got, a.ID, b.ID)
	}
}
