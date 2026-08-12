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
