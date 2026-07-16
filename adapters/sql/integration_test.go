//go:build integration

package changelogsql_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	changelogsql "github.com/zdirnecamlcs96/chronicle/adapters/sql"
	"github.com/zdirnecamlcs96/chronicle/core"
	"github.com/zdirnecamlcs96/chronicle/core/conformance"
)

// TestSQLLog_Conformance proves the SQL adapter is contract-equivalent to
// MemoryLog (same RunLogConformance suite) AND fork-free under concurrent
// same-document appends (RunSerializableAppend, which MemoryLog cannot pass).
// This is the "works no matter what database" gate.
//
//	CHANGELOG_SQL_TEST_DSN='root:root@tcp(127.0.0.1:3306)/changelog?parseTime=true' \
//	    go test -tags integration ./...
func TestSQLLog_Conformance(t *testing.T) {
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
	conformance.RunSerializableAppend(t, newLog)
	conformance.RunDeduperConformance(t, newLog)
	conformance.RunTailReaderConformance(t, newLog)
	conformance.RunSnapshotterConformance(t, newLog)
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

	if err := l.MarkSeen(ctx, "doc", "old-key", changelog.Commit{ID: "old-commit", At: old}); err != nil {
		t.Fatalf("mark seen old: %v", err)
	}
	if err := l.MarkSeen(ctx, "doc", "new-key", changelog.Commit{ID: "new-commit", At: recent}); err != nil {
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
