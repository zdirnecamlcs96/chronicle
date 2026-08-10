package changelogsql

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
)

// The DDL is the correctness contract: the constraints, not application code,
// are what order commits and scope content-addressed IDs. This guards against
// silently weakening them — notably the regression where a global UNIQUE(id)
// wrongly rejected two documents that legitimately share a content-hash id.
// Uniqueness must stay scoped to (doc_id, id). (doc_id, parent) must stay a
// PLAIN index: forks are legal recorded facts, a UNIQUE there would reject them.
func TestDDL_EncodesCorrectnessConstraints(t *testing.T) {
	ddl := strings.Join(MySQL.ddl(), "\n")
	for _, want := range []string{
		"PRIMARY KEY (doc_id, seq)",             // per-document ordering + lock target
		"UNIQUE KEY uq_commit_id (doc_id, id)",  // per-doc, NOT global: same content hash can recur across docs
		"KEY idx_doc_parent (doc_id, parent)",   // plain index: forks are legal, this only speeds parent lookups
		"KEY idx_id (id)",                       // keeps FindByID fast despite non-leftmost id
		"CREATE TABLE IF NOT EXISTS seen",       // durable idempotency
		"PRIMARY KEY (doc_id, idempotency_key)", // idempotency keys are scoped per document
		"CREATE TABLE IF NOT EXISTS snapshots",  // durable snapshot cache
		"PRIMARY KEY (doc_id)",                  // one snapshot per document, latest wins
	} {
		if !strings.Contains(ddl, want) {
			t.Errorf("ddl is missing the constraint %q", want)
		}
	}
	if strings.Contains(ddl, "uq_doc_parent") {
		t.Error("ddl still carries the anti-fork UNIQUE(doc_id, parent) — forks must be storable")
	}
}

func TestErrorClassifiers(t *testing.T) {
	dup := func(key string) error {
		return &mysql.MySQLError{Number: 1062, Message: "Duplicate entry 'x' for key '" + key + "'"}
	}
	cases := []struct {
		name string
		got  bool
		want bool
	}{
		{"dup on named key", isDuplicateOfKey(dup("uq_commit_id"), "uq_commit_id"), true},
		{"dup on table-prefixed key", isDuplicateOfKey(dup("commits.uq_commit_id"), "uq_commit_id"), true},
		{"dup on other key", isDuplicateOfKey(dup("PRIMARY"), "uq_commit_id"), false},
		{"value spoofing a key name is not a match", isDuplicateOfKey(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry 'uq_commit_id-1' for key 'PRIMARY'"}, "uq_commit_id"), false},
		{"deadlock is not dup", isDuplicateOfKey(&mysql.MySQLError{Number: 1213}, "PRIMARY"), false},
		{"deadlock 1213", isDeadlock(&mysql.MySQLError{Number: 1213}), true},
		{"dup is not deadlock", isDeadlock(dup("PRIMARY")), false},
		{"generic error", isDuplicateOfKey(errors.New("boom"), "PRIMARY"), false},
		{"nil", isDeadlock(nil), false},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}
