//go:build integration

package changelogclickhouse_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	changelogclickhouse "github.com/zdirnecamlcs96/chronicle/adapters/clickhouse"
	"github.com/zdirnecamlcs96/chronicle/core"
	"github.com/zdirnecamlcs96/chronicle/core/conformance"
)

// TestClickHouseLog_Conformance runs the MANDATORY Log contract against real
// ClickHouse, proving the abstraction holds on a columnar store — ForkAppend
// included: commits sharing a parent are stored as a fork, per the Log
// contract. Per-document ordering rests on `at`; see the package doc.
//
//	CHANGELOG_CLICKHOUSE_TEST_DSN='clickhouse://default:@127.0.0.1:9000/changelog' \
//	    go test -tags integration ./...
func TestClickHouseLog_Conformance(t *testing.T) {
	dsn := os.Getenv("CHANGELOG_CLICKHOUSE_TEST_DSN")
	if dsn == "" {
		t.Skip("set CHANGELOG_CLICKHOUSE_TEST_DSN to run")
	}
	db, err := sql.Open("clickhouse", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("ping %s: %v", dsn, err)
	}

	newLog := func(t *testing.T) (changelog.Log, func()) {
		l := changelogclickhouse.New(db)
		if err := l.Migrate(context.Background()); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		for _, tbl := range []string{"commits", "seen", "snapshots"} {
			if _, err := db.Exec("TRUNCATE TABLE IF EXISTS " + tbl); err != nil {
				t.Fatalf("truncate %s: %v", tbl, err)
			}
		}
		return l, func() {}
	}

	conformance.RunLogConformance(t, newLog)
	conformance.RunDeduperConformance(t, newLog)
	conformance.RunTailReaderConformance(t, newLog)
	conformance.RunSnapshotterConformance(t, newLog)
}

// TestClickHouseLog_PruneSeen proves PruneSeen deletes seen rows older than
// the cutoff and leaves newer ones. Unlike the SQL adapter, ClickHouse
// lightweight deletes are async mutations (see capability.go), so the test
// forces this one delete to finish synchronously via the mutations_sync=1
// query setting before asserting on Seen — otherwise the assertions would
// race the background mutation.
func TestClickHouseLog_PruneSeen(t *testing.T) {
	dsn := os.Getenv("CHANGELOG_CLICKHOUSE_TEST_DSN")
	if dsn == "" {
		t.Skip("set CHANGELOG_CLICKHOUSE_TEST_DSN to run")
	}
	db, err := sql.Open("clickhouse", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("ping %s: %v", dsn, err)
	}

	l := changelogclickhouse.New(db)
	ctx := context.Background()
	if err := l.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, tbl := range []string{"commits", "seen", "snapshots"} {
		if _, err := db.Exec("TRUNCATE TABLE IF EXISTS " + tbl); err != nil {
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

	syncCtx := clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{"mutations_sync": "1"}))
	if err := l.PruneSeen(syncCtx, cutoff); err != nil {
		t.Fatalf("prune seen: %v", err)
	}

	if _, ok, err := l.Seen(ctx, "doc", "old-key"); err != nil || ok {
		t.Fatalf("old-key: ok=%v err=%v, want gone", ok, err)
	}
	if _, ok, err := l.Seen(ctx, "doc", "new-key"); err != nil || !ok {
		t.Fatalf("new-key: ok=%v err=%v, want present", ok, err)
	}
}
