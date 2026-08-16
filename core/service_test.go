package changelog

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
)

func oneChange() []Change {
	return []Change{{Actor: "a", Path: "p", Kind: "put", To: "1"}}
}

func TestService_SealAndCommits(t *testing.T) {
	svc := NewService(newMemLog())
	ctx := context.Background()
	c, err := svc.Seal(ctx, "d1", oneChange(), "msg")
	if err != nil {
		t.Fatal(err)
	}
	if c.ID == "" || c.Message != "msg" {
		t.Fatalf("bad commit: %+v", c)
	}
	got, _ := svc.Commits(ctx, "d1", 0)
	if len(got) != 1 || got[0].ID != c.ID {
		t.Fatalf("commits=%+v", got)
	}
}

func TestService_SealEmptyChanges(t *testing.T) {
	svc := NewService(newMemLog())
	if _, err := svc.Seal(context.Background(), "d1", nil, ""); !errors.Is(err, ErrEmptyChanges) {
		t.Fatalf("err=%v want ErrEmptyChanges", err)
	}
}

func TestService_Idempotent(t *testing.T) {
	svc := NewService(newMemLog())
	ctx := context.Background()
	c1, _ := svc.Seal(ctx, "d1", oneChange(), "", WithIdempotencyKey("k1"))
	c2, _ := svc.Seal(ctx, "d1", oneChange(), "", WithIdempotencyKey("k1"))
	if c1.ID != c2.ID {
		t.Fatalf("idempotency: ids differ %s %s", c1.ID, c2.ID)
	}
	if got, _ := svc.Commits(ctx, "d1", 0); len(got) != 1 {
		t.Fatalf("dedup failed, stored=%d want 1", len(got))
	}
}

func TestService_IdempotencyKeyScopedToDoc(t *testing.T) {
	// The service must thread docID to the Deduper so a key reused on a DIFFERENT
	// document seals its own commit instead of replaying (and disclosing) the
	// first document's commit.
	svc := NewService(newMemLog())
	ctx := context.Background()
	_, _ = svc.Seal(ctx, "docA", oneChange(), "", WithIdempotencyKey("k"))
	if _, err := svc.Seal(ctx, "docB", oneChange(), "", WithIdempotencyKey("k")); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.Commits(ctx, "docB", 0)
	if len(got) != 1 {
		t.Fatalf("docB seal dropped by cross-doc key collision: stored=%d want 1", len(got))
	}
}

func TestService_WithSignerSignsSealedCommits(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(newMemLog()).(*service).WithSigner(priv, "key-1")
	ctx := context.Background()
	if _, err := svc.Seal(ctx, "d1", oneChange(), "msg"); err != nil {
		t.Fatal(err)
	}
	resolve := func(keyID string) (ed25519.PublicKey, bool) {
		if keyID == "key-1" {
			return pub, true
		}
		return nil, false
	}
	report, err := VerifySignatures(ctx, svc.(*service).log, "d1", resolve)
	if err != nil {
		t.Fatal(err)
	}
	if report.Total != 1 || report.Valid != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestService_AllCommits(t *testing.T) {
	// Delegates to the backend Indexer and aggregates across documents.
	svc := NewService(newMemLog())
	ctx := context.Background()
	_, _ = svc.Seal(ctx, "d1", oneChange(), "")
	_, _ = svc.Seal(ctx, "d2", oneChange(), "")
	all, err := svc.AllCommits(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("all=%d want 2", len(all))
	}
}

func TestService_GetFoundAndMissing(t *testing.T) {
	svc := NewService(newMemLog())
	ctx := context.Background()
	c, _ := svc.Seal(ctx, "d1", oneChange(), "")
	dc, ok, err := svc.Get(ctx, c.ID)
	if err != nil || !ok || dc.Commit.ID != c.ID || dc.DocID != "d1" {
		t.Fatalf("get found: dc=%+v ok=%v err=%v", dc, ok, err)
	}
	if _, ok, _ := svc.Get(ctx, "deadbeef"); ok {
		t.Fatal("expected missing commit")
	}
}

func TestService_SealWithParent(t *testing.T) {
	// WithSealParent anchors the commit to the asserted base even when another
	// commit has since become Head — the append records a fork, not an error.
	svc := NewService(newMemLog())
	ctx := context.Background()
	base, err := svc.Seal(ctx, "d1", oneChange(), "root")
	if err != nil {
		t.Fatal(err)
	}
	racer, err := svc.Seal(ctx, "d1", oneChange(), "racer")
	if err != nil {
		t.Fatal(err)
	}
	forked, err := svc.Seal(ctx, "d1", oneChange(), "anchored", WithSealParent(base.ID))
	if err != nil {
		t.Fatalf("anchored seal: %v", err)
	}
	if forked.Parent != base.ID {
		t.Fatalf("parent=%q want asserted base %q", forked.Parent, base.ID)
	}
	got, _ := svc.Commits(ctx, "d1", 0)
	if len(got) != 3 {
		t.Fatalf("stored=%d want 3 (racer %s must survive)", len(got), racer.ID)
	}
	if err := VerifyChain(got); err != nil {
		t.Fatalf("forked history must verify: %v", err)
	}
}

// capLog implements Indexer + Deduper so NewService should delegate cross-doc
// reads and dedup to the backend instead of its in-memory fallback.
type capLog struct {
	*memLog
	allCalled  bool
	seenCalled bool
}

func (c *capLog) AllCommits(ctx context.Context, limit int) ([]DocCommit, error) {
	c.allCalled = true
	return []DocCommit{{DocID: "sentinel", Commit: Commit{ID: "X"}}}, nil
}
func (c *capLog) FindByID(ctx context.Context, id string) (DocCommit, bool, error) {
	return DocCommit{}, false, nil
}
func (c *capLog) Seen(ctx context.Context, docID, key string) (Commit, bool, error) {
	c.seenCalled = true
	return Commit{}, false, nil
}
func (c *capLog) MarkSeen(ctx context.Context, docID, key string, cm Commit) error { return nil }

func TestService_DelegatesToCapabilities(t *testing.T) {
	cl := &capLog{memLog: newMemLog()}
	svc := NewService(cl)
	ctx := context.Background()
	all, _ := svc.AllCommits(ctx, 0)
	if !cl.allCalled || len(all) != 1 || all[0].DocID != "sentinel" {
		t.Fatalf("AllCommits not delegated to Indexer: %+v", all)
	}
	if _, err := svc.Seal(ctx, "d1", oneChange(), "", WithIdempotencyKey("k1")); err != nil {
		t.Fatal(err)
	}
	if !cl.seenCalled {
		t.Fatal("expected Deduper.Seen to be consulted")
	}
}
