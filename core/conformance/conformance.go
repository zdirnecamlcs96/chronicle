package conformance

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/zdirnecamlcs96/chronicle/core"
)

// NewLog returns a fresh, empty Log plus a teardown func. The suite calls it
// once per subtest. Backends with external state (e.g. a SQL database) truncate
// their tables / close their handle in teardown; in-memory backends return a
// no-op func.
type NewLog func(t *testing.T) (changelog.Log, func())

// RunLogConformance runs the mandatory changelog.Log contract against the
// backend produced by newLog. Every Log implementation must pass it — it is the
// executable specification of the port.
func RunLogConformance(t *testing.T, newLog NewLog) {
	t.Helper()

	t.Run("EmptyHead", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		h, err := log.Head(context.Background(), "missing")
		if err != nil {
			t.Fatalf("Head: %v", err)
		}
		if h != "" {
			t.Fatalf("Head of unknown doc = %q, want \"\"", h)
		}
	})

	t.Run("EmptyCommits", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		cs, err := log.Commits(context.Background(), "missing", 0)
		if err != nil {
			t.Fatalf("Commits: %v", err)
		}
		if len(cs) != 0 {
			t.Fatalf("Commits of unknown doc = %d rows, want 0", len(cs))
		}
	})

	t.Run("AppendThenHead", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		cs := sealN(t, log, "doc", 2)
		h, err := log.Head(context.Background(), "doc")
		if err != nil {
			t.Fatal(err)
		}
		if h != cs[1].ID {
			t.Fatalf("Head = %q, want last id %q", h, cs[1].ID)
		}
	})

	t.Run("ParentChaining", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		cs := sealN(t, log, "doc", 2)
		if cs[0].Parent != "" {
			t.Fatalf("root parent = %q, want \"\"", cs[0].Parent)
		}
		if cs[1].Parent != cs[0].ID {
			t.Fatalf("chain broken: c2.Parent = %q, want %q", cs[1].Parent, cs[0].ID)
		}
	})

	t.Run("ChainVerifies", func(t *testing.T) {
		// The stored chain must survive a full hash re-verification — proving the
		// backend round-trips commits losslessly (parent, message, changes).
		log, done := newLog(t)
		defer done()
		sealN(t, log, "doc", 3)
		if err := changelog.Verify(context.Background(), log, "doc"); err != nil {
			t.Fatalf("Verify over stored chain: %v", err)
		}
	})

	t.Run("CommitsNewestFirst", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		cs := sealN(t, log, "doc", 3)
		got, err := log.Commits(context.Background(), "doc", 0)
		if err != nil {
			t.Fatal(err)
		}
		assertIDs(t, got, cs[2].ID, cs[1].ID, cs[0].ID)
	})

	t.Run("CommitsLimit", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		cs := sealN(t, log, "doc", 3)
		ctx := context.Background()
		two, err := log.Commits(ctx, "doc", 2)
		if err != nil {
			t.Fatal(err)
		}
		assertIDs(t, two, cs[2].ID, cs[1].ID)
		if all, _ := log.Commits(ctx, "doc", 0); len(all) != 3 {
			t.Fatalf("limit<=0 = %d, want 3 (all)", len(all))
		}
		if all, _ := log.Commits(ctx, "doc", 99); len(all) != 3 {
			t.Fatalf("limit>count = %d, want 3", len(all))
		}
	})

	t.Run("PerDocIsolation", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		sealN(t, log, "docA", 2)
		b := sealN(t, log, "docB", 1)
		ctx := context.Background()
		if ca, _ := log.Commits(ctx, "docA", 0); len(ca) != 2 {
			t.Fatalf("docA = %d commits, want 2", len(ca))
		}
		cb, _ := log.Commits(ctx, "docB", 0)
		if len(cb) != 1 || cb[0].ID != b[0].ID {
			t.Fatalf("docB isolation broken: %v", ids(cb))
		}
	})

	t.Run("ForkAppend", func(t *testing.T) {
		// Two writers building on the same parent both land: the Log does no
		// concurrency control, so a fork is a recorded fact, not an error. Head
		// stays the latest commit by arrival.
		log, done := newLog(t)
		defer done()
		ctx := context.Background()
		root := sealN(t, log, "doc", 1)[0]

		var tick int64 = 10 // past sealN's clock, so arrival order is unambiguous
		rec := changelog.NewRecorder("doc", log).WithClock(func() time.Time { tick++; return time.Unix(tick, 0).UTC() })
		rec.Append(changelog.Change{Actor: "a", Path: "p", Kind: "put", To: "childA"})
		a, err := rec.Commit(ctx, changelog.WithParent(root.ID))
		if err != nil {
			t.Fatalf("first child: %v", err)
		}
		rec.Append(changelog.Change{Actor: "a", Path: "p", Kind: "put", To: "childB"})
		b, err := rec.Commit(ctx, changelog.WithParent(root.ID))
		if err != nil {
			t.Fatalf("forking child: %v", err)
		}

		got, err := log.Commits(ctx, "doc", 0)
		if err != nil {
			t.Fatal(err)
		}
		assertIDs(t, got, b.ID, a.ID, root.ID)
		if err := changelog.Verify(ctx, log, "doc"); err != nil {
			t.Fatalf("forked history must verify: %v", err)
		}
		h, err := log.Head(ctx, "doc")
		if err != nil {
			t.Fatal(err)
		}
		if h != b.ID {
			t.Fatalf("Head = %q, want last-arrived %q", h, b.ID)
		}
	})

	t.Run("SignatureFieldsRoundTrip", func(t *testing.T) {
		// SigKeyID/Signature live outside the hash preimage; a backend must still
		// store and return them verbatim, unlike the hashed fields ChainVerifies
		// exercises.
		log, done := newLog(t)
		defer done()
		ctx := context.Background()
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		rec := changelog.NewRecorder("doc", log).WithSigner(priv, "key-1")
		rec.Append(changelog.Change{Actor: "a", Path: "p", Kind: "put", To: "v"})
		sealed, err := rec.Commit(ctx)
		if err != nil {
			t.Fatalf("sealing signed commit: %v", err)
		}
		got, err := log.Commits(ctx, "doc", 0)
		if err != nil {
			t.Fatal(err)
		}
		if got[0].SigKeyID != sealed.SigKeyID || !bytes.Equal(got[0].Signature, sealed.Signature) {
			t.Fatalf("signature fields did not round-trip: got SigKeyID=%q Signature=%x, want SigKeyID=%q Signature=%x",
				got[0].SigKeyID, got[0].Signature, sealed.SigKeyID, sealed.Signature)
		}
	})

	t.Run("ContextCancellation", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := log.AppendCommit(ctx, "doc", changelog.Commit{ID: "x"}); !errors.Is(err, context.Canceled) {
			t.Fatalf("AppendCommit: want context.Canceled, got %v", err)
		}
		if _, err := log.Commits(ctx, "doc", 0); !errors.Is(err, context.Canceled) {
			t.Fatalf("Commits: want context.Canceled, got %v", err)
		}
		if _, err := log.Head(ctx, "doc"); !errors.Is(err, context.Canceled) {
			t.Fatalf("Head: want context.Canceled, got %v", err)
		}
	})
}

// RunDeduperConformance is the opt-in contract for backends that implement
// changelog.Deduper: idempotency keys are scoped PER DOCUMENT. A key marked on
// one document must not resolve on another — otherwise a replay would disclose,
// and dedup against, an unrelated document's commit. Run it only against a
// backend that implements Deduper — every shipped adapter does, the durable
// ones across a restart and adapters/memory in process.
func RunDeduperConformance(t *testing.T, newLog NewLog) {
	t.Helper()
	log, done := newLog(t)
	defer done()
	d, ok := log.(changelog.Deduper)
	if !ok {
		t.Skip("backend does not implement changelog.Deduper")
	}
	ctx := context.Background()

	a := sealN(t, log, "docA", 1)[0]
	b := sealN(t, log, "docB", 1)[0]

	if _, ok, err := d.Seen(ctx, "docA", "k"); err != nil || ok {
		t.Fatalf("unseen key: ok=%v err=%v, want false/nil", ok, err)
	}
	if err := d.MarkSeen(ctx, "docA", "k", a); err != nil {
		t.Fatalf("MarkSeen docA: %v", err)
	}
	got, ok, err := d.Seen(ctx, "docA", "k")
	if err != nil || !ok || got.ID != a.ID {
		t.Fatalf("docA Seen: got=%q ok=%v err=%v, want %q/true", got.ID, ok, err, a.ID)
	}
	// The same key on a different document must NOT leak docA's commit.
	if _, ok, _ := d.Seen(ctx, "docB", "k"); ok {
		t.Fatal("idempotency key leaked across documents: docB resolved docA's key")
	}
	// And it resolves to docB's own commit once marked there.
	if err := d.MarkSeen(ctx, "docB", "k", b); err != nil {
		t.Fatalf("MarkSeen docB: %v", err)
	}
	if got, ok, _ := d.Seen(ctx, "docB", "k"); !ok || got.ID != b.ID {
		t.Fatalf("docB Seen: got=%q ok=%v, want %q", got.ID, ok, b.ID)
	}
}

// RunTailReaderConformance is the opt-in contract for backends that implement
// changelog.TailReader: CommitsAfter returns the commits strictly after a
// cursor, OLDEST first (replay order), with "" meaning from the root, an
// unknown or foreign cursor failing with ErrNoSuchCommit, and limit honored.
func RunTailReaderConformance(t *testing.T, newLog NewLog) {
	t.Helper()

	tail := func(t *testing.T, log changelog.Log) changelog.TailReader {
		t.Helper()
		tr, ok := log.(changelog.TailReader)
		if !ok {
			t.Skip("backend does not implement changelog.TailReader")
		}
		return tr
	}

	t.Run("FromRootOldestFirst", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		cs := sealN(t, log, "doc", 4)
		got, err := tail(t, log).CommitsAfter(context.Background(), "doc", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		assertIDs(t, got, cs[0].ID, cs[1].ID, cs[2].ID, cs[3].ID)
	})

	t.Run("AfterMidCursor", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		cs := sealN(t, log, "doc", 4)
		got, err := tail(t, log).CommitsAfter(context.Background(), "doc", cs[1].ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		assertIDs(t, got, cs[2].ID, cs[3].ID)
	})

	t.Run("Limit", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		cs := sealN(t, log, "doc", 4)
		got, err := tail(t, log).CommitsAfter(context.Background(), "doc", cs[0].ID, 2)
		if err != nil {
			t.Fatal(err)
		}
		assertIDs(t, got, cs[1].ID, cs[2].ID)
	})

	t.Run("AfterHeadEmpty", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		cs := sealN(t, log, "doc", 2)
		got, err := tail(t, log).CommitsAfter(context.Background(), "doc", cs[1].ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("after head = %d commits, want 0", len(got))
		}
	})

	t.Run("UnknownCursor", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		sealN(t, log, "doc", 1)
		if _, err := tail(t, log).CommitsAfter(context.Background(), "doc", "nope", 0); !errors.Is(err, changelog.ErrNoSuchCommit) {
			t.Fatalf("unknown cursor: %v, want ErrNoSuchCommit", err)
		}
	})

	t.Run("CrossDocCursor", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		// docA gets TWO commits: identical root content on two documents yields
		// the SAME content-addressed ID, so only a[1] (chained past docA's root)
		// is guaranteed absent from docB.
		a := sealN(t, log, "docA", 2)
		sealN(t, log, "docB", 1)
		if _, err := tail(t, log).CommitsAfter(context.Background(), "docB", a[1].ID, 0); !errors.Is(err, changelog.ErrNoSuchCommit) {
			t.Fatalf("docA cursor on docB: %v, want ErrNoSuchCommit", err)
		}
	})

	t.Run("ContextCancellation", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		tr := tail(t, log)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := tr.CommitsAfter(ctx, "doc", "", 0); !errors.Is(err, context.Canceled) {
			t.Fatalf("CommitsAfter: want context.Canceled, got %v", err)
		}
	})
}

// RunSnapshotterConformance is the opt-in contract for backends that implement
// changelog.Snapshotter: one snapshot per document, latest write wins, bytes
// round-trip untouched, documents isolated.
func RunSnapshotterConformance(t *testing.T, newLog NewLog) {
	t.Helper()

	snap := func(t *testing.T, log changelog.Log) changelog.Snapshotter {
		t.Helper()
		s, ok := log.(changelog.Snapshotter)
		if !ok {
			t.Skip("backend does not implement changelog.Snapshotter")
		}
		return s
	}

	t.Run("AbsentIsNotOK", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		if _, ok, err := snap(t, log).LoadSnapshot(context.Background(), "missing"); err != nil || ok {
			t.Fatalf("absent snapshot: ok=%v err=%v, want false/nil", ok, err)
		}
	})

	t.Run("RoundTrip", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		s := snap(t, log)
		ctx := context.Background()
		in := changelog.Snapshot{DocID: "doc", CommitID: "c1", State: []byte(`{"a":1}`)}
		if err := s.SaveSnapshot(ctx, in); err != nil {
			t.Fatalf("SaveSnapshot: %v", err)
		}
		got, ok, err := s.LoadSnapshot(ctx, "doc")
		if err != nil || !ok {
			t.Fatalf("LoadSnapshot: ok=%v err=%v", ok, err)
		}
		if got.DocID != in.DocID || got.CommitID != in.CommitID || string(got.State) != string(in.State) {
			t.Fatalf("round-trip: got %+v, want %+v", got, in)
		}
	})

	t.Run("LatestWins", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		s := snap(t, log)
		ctx := context.Background()
		if err := s.SaveSnapshot(ctx, changelog.Snapshot{DocID: "doc", CommitID: "c1", State: []byte("old")}); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveSnapshot(ctx, changelog.Snapshot{DocID: "doc", CommitID: "c2", State: []byte("new")}); err != nil {
			t.Fatal(err)
		}
		got, ok, err := s.LoadSnapshot(ctx, "doc")
		if err != nil || !ok || got.CommitID != "c2" || string(got.State) != "new" {
			t.Fatalf("latest-wins: got %+v ok=%v err=%v, want c2/new", got, ok, err)
		}
	})

	t.Run("PerDocIsolation", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		s := snap(t, log)
		ctx := context.Background()
		if err := s.SaveSnapshot(ctx, changelog.Snapshot{DocID: "docA", CommitID: "ca", State: []byte("A")}); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := s.LoadSnapshot(ctx, "docB"); ok {
			t.Fatal("docB resolved docA's snapshot")
		}
	})

	t.Run("ContextCancellation", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		s := snap(t, log)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := s.SaveSnapshot(ctx, changelog.Snapshot{DocID: "doc"}); !errors.Is(err, context.Canceled) {
			t.Fatalf("SaveSnapshot: want context.Canceled, got %v", err)
		}
		if _, _, err := s.LoadSnapshot(ctx, "doc"); !errors.Is(err, context.Canceled) {
			t.Fatalf("LoadSnapshot: want context.Canceled, got %v", err)
		}
	})
}

// RunAnnotatorConformance is the opt-in contract for backends that implement
// changelog.Annotator: at most one annotation per (document, commit), latest
// write wins, bytes round-trip untouched, documents isolated, absent ids
// simply missing from the result map.
func RunAnnotatorConformance(t *testing.T, newLog NewLog) {
	t.Helper()

	ann := func(t *testing.T, log changelog.Log) changelog.Annotator {
		t.Helper()
		a, ok := log.(changelog.Annotator)
		if !ok {
			t.Skip("backend does not implement changelog.Annotator")
		}
		return a
	}

	t.Run("AbsentIsEmpty", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		got, err := ann(t, log).LoadAnnotations(context.Background(), "doc", []string{"missing"})
		if err != nil || len(got) != 0 {
			t.Fatalf("absent annotation: got %v err=%v, want empty/nil", got, err)
		}
	})

	t.Run("EmptyIDs", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		got, err := ann(t, log).LoadAnnotations(context.Background(), "doc", nil)
		if err != nil || len(got) != 0 {
			t.Fatalf("empty ids: got %v err=%v, want empty/nil", got, err)
		}
	})

	t.Run("RoundTrip", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		a := ann(t, log)
		ctx := context.Background()
		in := changelog.Annotation{DocID: "doc", CommitID: "c1", Data: []byte(`{"rows":[]}`)}
		if err := a.SaveAnnotation(ctx, in); err != nil {
			t.Fatalf("SaveAnnotation: %v", err)
		}
		got, err := a.LoadAnnotations(ctx, "doc", []string{"c1"})
		if err != nil {
			t.Fatalf("LoadAnnotations: %v", err)
		}
		if string(got["c1"]) != string(in.Data) {
			t.Fatalf("round-trip: got %q, want %q", got["c1"], in.Data)
		}
	})

	t.Run("LatestWins", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		a := ann(t, log)
		ctx := context.Background()
		if err := a.SaveAnnotation(ctx, changelog.Annotation{DocID: "doc", CommitID: "c1", Data: []byte("old")}); err != nil {
			t.Fatal(err)
		}
		if err := a.SaveAnnotation(ctx, changelog.Annotation{DocID: "doc", CommitID: "c1", Data: []byte("new")}); err != nil {
			t.Fatal(err)
		}
		got, err := a.LoadAnnotations(ctx, "doc", []string{"c1"})
		if err != nil || string(got["c1"]) != "new" {
			t.Fatalf("latest-wins: got %q err=%v, want new", got["c1"], err)
		}
	})

	t.Run("BatchSubset", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		a := ann(t, log)
		ctx := context.Background()
		for _, id := range []string{"c1", "c2", "c3"} {
			if err := a.SaveAnnotation(ctx, changelog.Annotation{DocID: "doc", CommitID: id, Data: []byte(id)}); err != nil {
				t.Fatal(err)
			}
		}
		got, err := a.LoadAnnotations(ctx, "doc", []string{"c1", "c3", "missing"})
		if err != nil {
			t.Fatalf("LoadAnnotations: %v", err)
		}
		if len(got) != 2 || string(got["c1"]) != "c1" || string(got["c3"]) != "c3" {
			t.Fatalf("batch subset: got %v, want exactly c1+c3", got)
		}
	})

	t.Run("PerDocIsolation", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		a := ann(t, log)
		ctx := context.Background()
		if err := a.SaveAnnotation(ctx, changelog.Annotation{DocID: "docA", CommitID: "c1", Data: []byte("A")}); err != nil {
			t.Fatal(err)
		}
		if got, _ := a.LoadAnnotations(ctx, "docB", []string{"c1"}); len(got) != 0 {
			t.Fatal("docB resolved docA's annotation")
		}
	})

	t.Run("ContextCancellation", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		a := ann(t, log)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := a.SaveAnnotation(ctx, changelog.Annotation{DocID: "doc", CommitID: "c1"}); !errors.Is(err, context.Canceled) {
			t.Fatalf("SaveAnnotation: want context.Canceled, got %v", err)
		}
		if _, err := a.LoadAnnotations(ctx, "doc", []string{"c1"}); !errors.Is(err, context.Canceled) {
			t.Fatalf("LoadAnnotations: want context.Canceled, got %v", err)
		}
	})
}

// RunTipperConformance is the opt-in contract for backends that implement
// changelog.Tipper: Tips returns the commit ids no other commit lists as
// parent, in chronological (append) order — one for a linear chain (equal to
// Head), one per branch under a fork — and an unknown document returns an
// empty slice, nil error.
func RunTipperConformance(t *testing.T, newLog NewLog) {
	t.Helper()

	tip := func(t *testing.T, log changelog.Log) changelog.Tipper {
		t.Helper()
		tp, ok := log.(changelog.Tipper)
		if !ok {
			t.Skip("backend does not implement changelog.Tipper")
		}
		return tp
	}

	t.Run("UnknownDocEmpty", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		got, err := tip(t, log).Tips(context.Background(), "missing")
		if err != nil || len(got) != 0 {
			t.Fatalf("unknown doc: got %v err=%v, want empty/nil", got, err)
		}
	})

	t.Run("LinearOneTipEqualsHead", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		ctx := context.Background()
		cs := sealN(t, log, "doc", 3)
		got, err := tip(t, log).Tips(ctx, "doc")
		if err != nil {
			t.Fatal(err)
		}
		h, err := log.Head(ctx, "doc")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0] != h || got[0] != cs[2].ID {
			t.Fatalf("linear tips = %v, want [%q] (Head)", got, h)
		}
	})

	t.Run("ForkTwoTipsChronological", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		ctx := context.Background()
		root := sealN(t, log, "doc", 1)[0]

		var tick int64 = 10 // past sealN's clock, so arrival order is unambiguous
		rec := changelog.NewRecorder("doc", log).WithClock(func() time.Time { tick++; return time.Unix(tick, 0).UTC() })
		rec.Append(changelog.Change{Actor: "a", Path: "p", Kind: "put", To: "childA"})
		a, err := rec.Commit(ctx, changelog.WithParent(root.ID))
		if err != nil {
			t.Fatalf("first child: %v", err)
		}
		rec.Append(changelog.Change{Actor: "a", Path: "p", Kind: "put", To: "childB"})
		b, err := rec.Commit(ctx, changelog.WithParent(root.ID))
		if err != nil {
			t.Fatalf("forking child: %v", err)
		}

		got, err := tip(t, log).Tips(ctx, "doc")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0] != a.ID || got[1] != b.ID {
			t.Fatalf("fork tips = %v, want [%q %q] (chronological)", got, a.ID, b.ID)
		}
	})

	t.Run("ContextCancellation", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		tp := tip(t, log)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := tp.Tips(ctx, "doc"); !errors.Is(err, context.Canceled) {
			t.Fatalf("Tips: want context.Canceled, got %v", err)
		}
	})
}

// sealN seals n chained commits into log via a Recorder with a MONOTONIC clock,
// so every commit gets a distinct, increasing timestamp. This keeps the suite
// portable: backends that order by seq (SQL) AND backends that order by
// timestamp (ClickHouse, columnar) both return a deterministic newest-first.
func sealN(t *testing.T, log changelog.Log, docID string, n int) []changelog.Commit {
	t.Helper()
	ctx := context.Background()
	var tick int64
	monotonic := func() time.Time { tick++; return time.Unix(tick, 0).UTC() }
	rec := changelog.NewRecorder(docID, log).WithClock(monotonic)
	out := make([]changelog.Commit, 0, n)
	for i := 0; i < n; i++ {
		rec.Append(changelog.Change{Actor: "a", Path: "p", Kind: "put", To: fmt.Sprintf("v%d", i)})
		c, err := rec.Commit(ctx)
		if err != nil {
			t.Fatalf("seal commit %d on %q: %v", i, docID, err)
		}
		out = append(out, c)
	}
	return out
}

func assertIDs(t *testing.T, got []changelog.Commit, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d commits %v, want %d", len(got), ids(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i] {
			t.Fatalf("commit[%d].ID = %q, want %q (got %v)", i, got[i].ID, want[i], ids(got))
		}
	}
}

func ids(cs []changelog.Commit) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}
