//go:build integration

package changelogclickhouse_test

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/ClickHouse/clickhouse-go/v2"
	changelogclickhouse "github.com/zdirnecamlcs96/chronicle/adapters/clickhouse"
	"github.com/zdirnecamlcs96/chronicle/core"
	"github.com/zdirnecamlcs96/chronicle/core/conformance"
)

// TestClickHouseLog_Conformance runs the MANDATORY Log contract against real
// ClickHouse, proving the abstraction holds on a columnar store. It deliberately
// does NOT run RunSerializableAppend: ClickHouse cannot serialize concurrent
// same-doc appends (no locks/transactions/unique constraints) — producers must
// serialize per document. See the package doc.
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
		for _, tbl := range []string{"commits", "seen"} {
			if _, err := db.Exec("TRUNCATE TABLE IF EXISTS " + tbl); err != nil {
				t.Fatalf("truncate %s: %v", tbl, err)
			}
		}
		return l, func() {}
	}

	conformance.RunLogConformance(t, newLog)
	conformance.RunDeduperConformance(t, newLog)
}
