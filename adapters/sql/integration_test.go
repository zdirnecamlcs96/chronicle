//go:build integration

package changelogsql_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	changelogsql "github.com/zdirnecamlcs96/chronicle/adapters/sql"
	"github.com/zdirnecamlcs96/chronicle/core"
	"github.com/zdirnecamlcs96/chronicle/core/conformance"
)

// TestSQLLog_Conformance proves the SQL adapter is contract-equivalent to
// MemoryLog: the same RunLogConformance suite, ForkAppend included — two
// commits sharing a parent are stored as a fork, not rejected. This is the
// "works no matter what database" gate.
//
//	CHANGELOG_SQL_TEST_DSN='root:root@tcp(127.0.0.1:3306)/changelog?parseTime=true' \
//	    go test -tags integration ./...
func TestSQLLog_Conformance(t *testing.T) {
	db := testDB(t)

	// newLog returns a fresh (truncated) backend per subtest, sharing one pool.
	newLog := func(t *testing.T) (changelog.Log, func()) {
		l := changelogsql.New(db)
		if err := l.Migrate(context.Background()); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		for _, tbl := range []string{"commits", "seen", "snapshots"} {
			if _, err := db.Exec("TRUNCATE TABLE " + tbl); err != nil {
				t.Fatalf("truncate %s: %v", tbl, err)
			}
		}
		return l, func() {}
	}

	conformance.RunLogConformance(t, newLog)
	conformance.RunDeduperConformance(t, newLog)
	conformance.RunTailReaderConformance(t, newLog)
	conformance.RunSnapshotterConformance(t, newLog)
	conformance.RunAnnotatorConformance(t, newLog)
}

// testDB opens the integration database or skips the test.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("CHANGELOG_SQL_TEST_DSN")
	if dsn == "" {
		t.Skip("set CHANGELOG_SQL_TEST_DSN to run (e.g. root:root@tcp(127.0.0.1:3306)/changelog?parseTime=true)")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("ping %s: %v", dsn, err)
	}
	return db
}

// freshLog migrates and truncates, returning a clean adapter over db.
func freshLog(t *testing.T, db *sql.DB) *changelogsql.Log {
	t.Helper()
	l := changelogsql.New(db)
	if err := l.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, tbl := range []string{"commits", "seen", "snapshots"} {
		if _, err := db.Exec("TRUNCATE TABLE " + tbl); err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}
	return l
}

// TestSQLLog_IdempotentReplay proves re-appending an identical commit (same
// document, same content-hash id) is a no-op: one row, nil error — the
// at-least-once delivery contract.
func TestSQLLog_IdempotentReplay(t *testing.T) {
	db := testDB(t)
	l := freshLog(t, db)
	ctx := context.Background()

	rec := changelog.NewRecorder("doc", l)
	rec.Append(changelog.Change{Actor: "a", Path: "p", Kind: "put", To: "v"})
	c, err := rec.Commit(ctx)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if err := l.AppendCommit(ctx, "doc", c); err != nil {
		t.Fatalf("replay of identical commit must be a no-op, got %v", err)
	}
	got, err := l.Commits(ctx, "doc", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("stored %d rows, want 1", len(got))
	}
}

// TestSQLLog_ConcurrentAppendAllSucceed hammers one EMPTY document: every
// writer must land (the gap-lock PRIMARY KEY race is retried internally), all
// rows with distinct seq. Forks among them are legal recorded facts.
func TestSQLLog_ConcurrentAppendAllSucceed(t *testing.T) {
	db := testDB(t)
	l := freshLog(t, db)
	ctx := context.Background()

	const writers = 8
	start := make(chan struct{})
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := changelog.NewRecorder("doc", l)
			rec.Append(changelog.Change{Actor: "a", Path: "p", Kind: "put", To: fmt.Sprintf("v%d", i)})
			<-start
			_, err := rec.Commit(ctx)
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("append must always succeed under the fork model, got %v", err)
		}
	}

	commits, err := l.Commits(ctx, "doc", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != writers {
		t.Fatalf("stored %d commits, want %d", len(commits), writers)
	}
	if err := changelog.Verify(ctx, l, "doc"); err != nil {
		t.Fatalf("hammered history must verify: %v", err)
	}
}

// TestSQLLog_ConcurrentSameDocHammer hammers one document with many writers
// each doing several sequential commits: every append must succeed and the
// resulting chain must hold exactly writers*perWriter commits and verify
// clean. Regression guard for the doc_locks per-document serialization.
func TestSQLLog_ConcurrentSameDocHammer(t *testing.T) {
	db := testDB(t)
	l := freshLog(t, db)
	ctx := context.Background()

	const writers = 8
	const perWriter = 10
	start := make(chan struct{})
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := changelog.NewRecorder("hammer-doc", l)
			<-start
			for j := 0; j < perWriter; j++ {
				rec.Append(changelog.Change{Actor: "a", Path: "p", Kind: "put", To: fmt.Sprintf("v%d-%d", i, j)})
				if _, err := rec.Commit(ctx); err != nil {
					errs <- err
					return
				}
			}
			errs <- nil
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("append must always succeed under the fork model, got %v", err)
		}
	}

	commits, err := l.Commits(ctx, "hammer-doc", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != writers*perWriter {
		t.Fatalf("stored %d commits, want %d", len(commits), writers*perWriter)
	}
	if err := changelog.Verify(ctx, l, "hammer-doc"); err != nil {
		t.Fatalf("hammered history must verify: %v", err)
	}
}

// TestSQLLog_ConcurrentNewDocBurst creates many BRAND-NEW documents at once,
// with doc_ids chosen to sort adjacently so their first head reads land in the
// same commits index gap. Regression guard for the cross-document gap-lock
// deadlock the head read's FOR UPDATE used to cause (see appendOnce).
func TestSQLLog_ConcurrentNewDocBurst(t *testing.T) {
	db := testDB(t)
	l := freshLog(t, db)
	ctx := context.Background()

	const writers = 8
	start := make(chan struct{})
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			docID := fmt.Sprintf("burst-%03d", i)
			rec := changelog.NewRecorder(docID, l)
			rec.Append(changelog.Change{Actor: "a", Path: "p", Kind: "put", To: "root"})
			<-start
			_, err := rec.Commit(ctx)
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("first append to a distinct new document must always succeed, got %v", err)
		}
	}

	for i := 0; i < writers; i++ {
		docID := fmt.Sprintf("burst-%03d", i)
		commits, err := l.Commits(ctx, docID, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(commits) != 1 {
			t.Fatalf("doc %s stored %d commits, want 1", docID, len(commits))
		}
	}
}

// TestSQLLog_MigrateFromAntiForkSchema proves Migrate upgrades a database that
// still carries the pre-fork-tolerance UNIQUE(doc_id, parent): the constraint
// is swapped for a plain index and a fork then stores cleanly.
func TestSQLLog_MigrateFromAntiForkSchema(t *testing.T) {
	db := testDB(t)
	l := freshLog(t, db)
	ctx := context.Background()

	// Regress the schema to the old anti-fork shape, then migrate again.
	if _, err := db.Exec(`ALTER TABLE commits DROP INDEX idx_doc_parent, ADD UNIQUE INDEX uq_doc_parent (doc_id, parent)`); err != nil {
		t.Fatalf("install old schema: %v", err)
	}
	if err := l.Migrate(ctx); err != nil {
		t.Fatalf("migrate from old schema: %v", err)
	}

	rec := changelog.NewRecorder("doc", l)
	rec.Append(changelog.Change{Actor: "a", Path: "p", Kind: "put", To: "root"})
	root, err := rec.Commit(ctx)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	for _, v := range []string{"childA", "childB"} {
		rec.Append(changelog.Change{Actor: "a", Path: "p", Kind: "put", To: v})
		if _, err := rec.Commit(ctx, changelog.WithParent(root.ID)); err != nil {
			t.Fatalf("fork child %s after migrate: %v", v, err)
		}
	}
	if got, _ := l.Commits(ctx, "doc", 0); len(got) != 3 {
		t.Fatalf("stored %d commits, want 3 (fork must be storable after migrate)", len(got))
	}
}

// TestSQLLog_MigrateFromPreSignatureSchema proves Migrate adds the
// sig_key_id/signature columns to a commits table that predates them, and
// that a signed commit then stores and reads back correctly.
func TestSQLLog_MigrateFromPreSignatureSchema(t *testing.T) {
	db := testDB(t)
	l := freshLog(t, db)
	ctx := context.Background()

	// Regress the schema to the pre-signature shape, then migrate again.
	if _, err := db.Exec(`ALTER TABLE commits DROP COLUMN sig_key_id, DROP COLUMN signature`); err != nil {
		t.Fatalf("install old schema: %v", err)
	}
	if err := l.Migrate(ctx); err != nil {
		t.Fatalf("migrate from old schema: %v", err)
	}

	c := changelog.Commit{ID: "root", At: time.Now().UTC(), SigKeyID: "key-1", Signature: []byte("sig-bytes")}
	if err := l.AppendCommit(ctx, "doc", c); err != nil {
		t.Fatalf("append after migrate: %v", err)
	}
	got, err := l.Commits(ctx, "doc", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SigKeyID != c.SigKeyID || string(got[0].Signature) != string(c.Signature) {
		t.Fatalf("signature fields after migrate = %+v, want SigKeyID=%q Signature=%q", got, c.SigKeyID, c.Signature)
	}
}

// TestSQLLog_Tips proves Tips returns the commit ids no other commit lists as
// parent, chronological: one for a linear chain (equal to Head), one per
// branch under a fork. Called directly on the concrete type — Tips is not yet
// a core.Tipper the pinned core release declares (see capability.go).
func TestSQLLog_Tips(t *testing.T) {
	db := testDB(t)
	l := freshLog(t, db)
	ctx := context.Background()

	if got, err := l.Tips(ctx, "missing"); err != nil || len(got) != 0 {
		t.Fatalf("unknown doc: got %v err=%v, want empty/nil", got, err)
	}

	rec := changelog.NewRecorder("doc", l)
	rec.Append(changelog.Change{Actor: "a", Path: "p", Kind: "put", To: "1"})
	if _, err := rec.Commit(ctx); err != nil {
		t.Fatalf("root: %v", err)
	}
	rec.Append(changelog.Change{Actor: "a", Path: "p", Kind: "put", To: "2"})
	mid, err := rec.Commit(ctx)
	if err != nil {
		t.Fatalf("mid: %v", err)
	}
	if got, err := l.Tips(ctx, "doc"); err != nil || len(got) != 1 || got[0] != mid.ID {
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
	got, err := l.Tips(ctx, "doc")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != a.ID || got[1] != b.ID {
		t.Fatalf("fork tips = %v, want [%q %q] (chronological)", got, a.ID, b.ID)
	}
}

// TestSQLLog_PruneSeen proves PruneSeen deletes only seen rows older than the
// cutoff, batching notwithstanding, and reports the count it removed. MarkSeen
// stores the commit's own At as the row's timestamp (see capability.go), so the
// test controls "old" and "new" by choosing each commit's At directly, no need
// to poke the seen table with raw SQL.
func TestSQLLog_PruneSeen(t *testing.T) {
	dsn := os.Getenv("CHANGELOG_SQL_TEST_DSN")
	if dsn == "" {
		t.Skip("set CHANGELOG_SQL_TEST_DSN to run (e.g. root:root@tcp(127.0.0.1:3306)/changelog?parseTime=true)")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("ping %s: %v", dsn, err)
	}

	l := changelogsql.New(db)
	ctx := context.Background()
	if err := l.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, tbl := range []string{"commits", "seen", "snapshots"} {
		if _, err := db.Exec("TRUNCATE TABLE " + tbl); err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}

	now := time.Now().UTC()
	old := now.Add(-2 * time.Hour)
	recent := now.Add(-30 * time.Minute)
	cutoff := now.Add(-time.Hour)

	// Seen resolves the stored commit_id against the commits table, so the
	// commits must exist for the surviving key to read back as present.
	oldCommit := changelog.Commit{ID: "old-commit", At: old}
	newCommit := changelog.Commit{ID: "new-commit", Parent: "old-commit", At: recent}
	if err := l.AppendCommit(ctx, "doc", oldCommit); err != nil {
		t.Fatalf("append old: %v", err)
	}
	if err := l.AppendCommit(ctx, "doc", newCommit); err != nil {
		t.Fatalf("append new: %v", err)
	}
	if err := l.MarkSeen(ctx, "doc", "old-key", oldCommit); err != nil {
		t.Fatalf("mark seen old: %v", err)
	}
	if err := l.MarkSeen(ctx, "doc", "new-key", newCommit); err != nil {
		t.Fatalf("mark seen new: %v", err)
	}

	n, err := l.PruneSeen(ctx, cutoff)
	if err != nil {
		t.Fatalf("prune seen: %v", err)
	}
	if n != 1 {
		t.Fatalf("prune seen = %d, want 1", n)
	}

	if _, ok, err := l.Seen(ctx, "doc", "old-key"); err != nil || ok {
		t.Fatalf("old-key: ok=%v err=%v, want gone", ok, err)
	}
	if _, ok, err := l.Seen(ctx, "doc", "new-key"); err != nil || !ok {
		t.Fatalf("new-key: ok=%v err=%v, want present", ok, err)
	}
}
