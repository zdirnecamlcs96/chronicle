//go:build integration

package changelogsql_test

import (
	"context"
	"database/sql"
	"os"
	"testing"

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
		for _, tbl := range []string{"commits", "seen"} {
			if _, err := db.Exec("TRUNCATE TABLE " + tbl); err != nil {
				t.Fatalf("truncate %s: %v", tbl, err)
			}
		}
		return l, func() {}
	}

	conformance.RunLogConformance(t, newLog)
	conformance.RunSerializableAppend(t, newLog)
}
