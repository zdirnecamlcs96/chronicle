package changelog

import (
	"context"
	"encoding/binary"
	"sync"
	"testing"
	"time"
)

// sealN appends n commits to docID in log using a monotonic clock, returning
// their ids in append (oldest-first) order.
func sealN(t *testing.T, log Log, docID string, n int) []string {
	t.Helper()
	var tick int64
	rec := NewRecorder(docID, log).WithClock(func() time.Time { tick++; return time.Unix(tick, 0).UTC() })
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		rec.Append(Change{Actor: "a", Path: "p", Kind: "put", To: "v"})
		c, err := rec.Commit(context.Background(), WithMessage("m"))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, c.ID)
	}
	return ids
}

// TestCheckpointDigest_Golden pins the exact checkpoint digest for a fixed
// inventory. This hash is pinned forever. If this test fails, the preimage
// encoding changed and any anchored checkpoint no longer verifies — bump the
// format version instead.
func TestCheckpointDigest_Golden(t *testing.T) {
	inv := []DocState{
		{DocID: "doc-a", Heads: []string{"h2", "h1"}, Commits: 3},
		{DocID: "doc-b", Heads: []string{"h3"}, Commits: 1},
	}
	want := "0fd7f01a2067b18672fd047a7db33af4b04920ce58eb6d6a76602dfcd98a0239"
	if got := checkpointDigest(inv); got != want {
		t.Fatalf("golden mismatch: got %s want %s", got, want)
	}
}

// TestCheckpointDigest_NoHeadCommitsCollision regression-tests the framing
// bug where a raw Commits value was byte-indistinguishable from a head's own
// length prefix: without an explicit head count, {DocID:"a", no heads,
// Commits:64} followed by {DocID:"b"*56, no heads, Commits:7} hashed the same
// bytes as the single entry {DocID:"a", Heads:[<64-byte string built from an
// 8-byte big-endian 56 followed by "b"*56>], Commits:7} — the "Commits=64"
// field doubled as a fake head-length prefix for the next entry's DocID
// frame. The head count added to the encoding must make these differ.
func TestCheckpointDigest_NoHeadCommitsCollision(t *testing.T) {
	bs := make([]byte, 56)
	for i := range bs {
		bs[i] = 'b'
	}
	bStr := string(bs)

	lenPrefix := make([]byte, 8)
	binary.BigEndian.PutUint64(lenPrefix, 56)
	headContent := string(lenPrefix) + bStr

	a := []DocState{
		{DocID: "a", Commits: 64},
		{DocID: bStr, Commits: 7},
	}
	b := []DocState{
		{DocID: "a", Heads: []string{headContent}, Commits: 7},
	}
	if checkpointDigest(a) == checkpointDigest(b) {
		t.Fatal("two different inventories must not hash to the same digest")
	}
}

// TestCheckpointDigest_OrderIndependent checks that entry order and per-doc
// Heads order do not affect the digest — only the canonical (sorted) form
// does, since a backend's return order is not trusted.
func TestCheckpointDigest_OrderIndependent(t *testing.T) {
	a := []DocState{
		{DocID: "doc-a", Heads: []string{"h2", "h1"}, Commits: 3},
		{DocID: "doc-b", Heads: []string{"h3"}, Commits: 1},
	}
	b := []DocState{
		{DocID: "doc-b", Heads: []string{"h3"}, Commits: 1},
		{DocID: "doc-a", Heads: []string{"h1", "h2"}, Commits: 3},
	}
	if checkpointDigest(a) != checkpointDigest(b) {
		t.Fatal("digest must be independent of entry and head order")
	}
}

func TestComputeCheckpoint_RequiresIndexer(t *testing.T) {
	base := newMemLog()
	sealN(t, base, "doc", 1)
	if _, err := ComputeCheckpoint(context.Background(), tailless{base}); err == nil {
		t.Fatal("want error for a Log without Indexer")
	}
}

func TestVerifyCheckpoint_RequiresIndexer(t *testing.T) {
	base := newMemLog()
	sealN(t, base, "doc", 1)
	cp, err := ComputeCheckpoint(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyCheckpoint(context.Background(), tailless{base}, cp); err == nil {
		t.Fatal("want error for a Log without Indexer")
	}
}

func TestCheckpoint_RoundTrip_Clean(t *testing.T) {
	ctx := context.Background()
	log := newMemLog()
	sealN(t, log, "doc-a", 3)
	sealN(t, log, "doc-b", 1)

	cp, err := ComputeCheckpoint(ctx, log)
	if err != nil {
		t.Fatal(err)
	}
	if len(cp.Inventory) != 2 {
		t.Fatalf("want 2 documents, got %d", len(cp.Inventory))
	}

	report, err := VerifyCheckpoint(ctx, log, cp)
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK || report.Doctored {
		t.Fatalf("clean round trip must be OK: %+v", report)
	}
}

func TestCheckpoint_LegitGrowthAfterCheckpoint(t *testing.T) {
	ctx := context.Background()
	log := newMemLog()
	sealN(t, log, "doc-a", 2)

	cp, err := ComputeCheckpoint(ctx, log)
	if err != nil {
		t.Fatal(err)
	}

	// Growth on the checkpointed document, and a brand-new document — both legal.
	sealN(t, log, "doc-a", 2)
	sealN(t, log, "doc-new", 1)

	report, err := VerifyCheckpoint(ctx, log, cp)
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK {
		t.Fatalf("legitimate growth must still verify OK: %+v", report)
	}
}

func TestCheckpoint_WholeDocumentDeleted(t *testing.T) {
	ctx := context.Background()
	log := newMemLog()
	sealN(t, log, "doc-a", 2)
	sealN(t, log, "doc-b", 1)

	cp, err := ComputeCheckpoint(ctx, log)
	if err != nil {
		t.Fatal(err)
	}

	log.mu.Lock()
	delete(log.commits, "doc-b")
	log.mu.Unlock()

	report, err := VerifyCheckpoint(ctx, log, cp)
	if err != nil {
		t.Fatal(err)
	}
	if report.OK {
		t.Fatal("deleted document must fail verification")
	}
	if len(report.MissingDocs) != 1 || report.MissingDocs[0] != "doc-b" {
		t.Fatalf("MissingDocs = %v, want [doc-b]", report.MissingDocs)
	}
}

func TestCheckpoint_TailTruncated(t *testing.T) {
	ctx := context.Background()
	log := newMemLog()
	sealN(t, log, "doc-a", 3)

	cp, err := ComputeCheckpoint(ctx, log)
	if err != nil {
		t.Fatal(err)
	}

	// Drop the tip commit — the recorded head no longer exists anywhere.
	log.mu.Lock()
	src := log.commits["doc-a"]
	log.commits["doc-a"] = src[:len(src)-1]
	log.mu.Unlock()

	report, err := VerifyCheckpoint(ctx, log, cp)
	if err != nil {
		t.Fatal(err)
	}
	if report.OK {
		t.Fatal("truncated tail must fail verification")
	}
	if missing := report.MissingHeads["doc-a"]; len(missing) != 1 {
		t.Fatalf("MissingHeads[doc-a] = %v, want exactly the dropped head", missing)
	}
}

func TestCheckpoint_InteriorDeletionShrinksCount(t *testing.T) {
	ctx := context.Background()
	log := newMemLog()
	sealN(t, log, "doc-a", 3)

	cp, err := ComputeCheckpoint(ctx, log)
	if err != nil {
		t.Fatal(err)
	}

	// Remove a mid-chain commit; the head is untouched, so only the count moves.
	log.mu.Lock()
	src := log.commits["doc-a"]
	log.commits["doc-a"] = append(append([]Commit(nil), src[:1]...), src[2:]...)
	log.mu.Unlock()

	report, err := VerifyCheckpoint(ctx, log, cp)
	if err != nil {
		t.Fatal(err)
	}
	if report.OK {
		t.Fatal("interior deletion must fail verification")
	}
	if len(report.MissingHeads["doc-a"]) != 0 {
		t.Fatalf("head is untouched by an interior deletion, got MissingHeads %v", report.MissingHeads)
	}
	if len(report.ShrunkDocs) != 1 || report.ShrunkDocs[0].DocID != "doc-a" || report.ShrunkDocs[0].Recorded != 3 || report.ShrunkDocs[0].Current != 2 {
		t.Fatalf("ShrunkDocs = %+v, want one entry doc-a 3->2", report.ShrunkDocs)
	}
}

func TestCheckpoint_DoctoredCheckpointCaughtBeforeLogComparison(t *testing.T) {
	ctx := context.Background()
	log := newMemLog()
	sealN(t, log, "doc-a", 2)

	cp, err := ComputeCheckpoint(ctx, log)
	if err != nil {
		t.Fatal(err)
	}
	// Edit the inventory without recomputing the digest.
	cp.Inventory[0].Commits = 999

	// Pass a Log that hides Indexer: if VerifyCheckpoint reached the log
	// comparison at all, it would fail on the missing capability, not report
	// Doctored — proving the digest check runs first.
	report, err := VerifyCheckpoint(ctx, tailless{log}, cp)
	if err != nil {
		t.Fatalf("doctored checkpoint must be a report finding, not an error: %v", err)
	}
	if !report.Doctored || report.OK {
		t.Fatalf("want Doctored report, got %+v", report)
	}
}

// fakeAnchorer is a test-only Anchorer: an in-memory stand-in for wherever a
// real deployment would anchor externally (another org's store, WORM
// storage, ...). Core ships no real implementation.
type fakeAnchorer struct {
	mu  sync.Mutex
	cp  Checkpoint
	has bool
}

func (f *fakeAnchorer) Anchor(ctx context.Context, cp Checkpoint) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cp, f.has = cp, true
	return nil
}

func (f *fakeAnchorer) LatestAnchor(ctx context.Context) (Checkpoint, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cp, f.has, nil
}

var _ Anchorer = (*fakeAnchorer)(nil)

func TestFakeAnchorer_RoundTrip(t *testing.T) {
	ctx := context.Background()
	a := &fakeAnchorer{}

	if _, ok, err := a.LatestAnchor(ctx); err != nil || ok {
		t.Fatalf("empty anchorer: ok=%v err=%v, want false, nil", ok, err)
	}

	log := newMemLog()
	sealN(t, log, "doc-a", 1)
	cp, err := ComputeCheckpoint(ctx, log)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Anchor(ctx, cp); err != nil {
		t.Fatal(err)
	}

	got, ok, err := a.LatestAnchor(ctx)
	if err != nil || !ok {
		t.Fatalf("LatestAnchor: ok=%v err=%v, want true, nil", ok, err)
	}
	if got.Digest != cp.Digest {
		t.Fatalf("LatestAnchor digest = %s, want %s", got.Digest, cp.Digest)
	}
}
