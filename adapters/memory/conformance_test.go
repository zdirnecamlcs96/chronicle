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
