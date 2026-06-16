package changelogsql

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
)

// The DDL is the correctness contract: the constraints, not application code,
// are what prevent forks and over-constrain content-addressed IDs. This guards
// against silently weakening them — notably the regression where a global
// UNIQUE(id) wrongly rejected two documents that legitimately share a
// content-hash id. Uniqueness must stay scoped to (doc_id, id).
func TestDDL_EncodesCorrectnessConstraints(t *testing.T) {
	ddl := strings.Join(MySQL.ddl(), "\n")
	for _, want := range []string{
		"PRIMARY KEY (doc_id, seq)",                 // per-document ordering + lock target
		"UNIQUE KEY uq_commit_id (doc_id, id)",      // per-doc, NOT global: same content hash can recur across docs
		"UNIQUE KEY uq_doc_parent (doc_id, parent)", // anti-fork: one child per parent, one root per doc
		"KEY idx_id (id)",                           // keeps FindByID fast despite non-leftmost id
		"CREATE TABLE IF NOT EXISTS seen",           // durable idempotency
		"PRIMARY KEY (idempotency_key)",
	} {
		if !strings.Contains(ddl, want) {
			t.Errorf("ddl is missing the constraint %q", want)
		}
	}
}

func TestIsDuplicate(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"dup entry 1062", &mysql.MySQLError{Number: 1062}, true},
		{"deadlock 1213", &mysql.MySQLError{Number: 1213}, false},
		{"generic error", errors.New("boom"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		if got := isDuplicate(tc.err); got != tc.want {
			t.Errorf("%s: isDuplicate = %v, want %v", tc.name, got, tc.want)
		}
	}
}
