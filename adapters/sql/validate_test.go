package changelogsql

import (
	"context"
	"strings"
	"testing"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
)

// The schema stores doc_id and idempotency_key as VARCHAR(255). On a MySQL
// server not in strict mode an over-length value is silently truncated, which
// would collapse two distinct ids into one row. The adapter rejects the write
// before it reaches the database. New(nil) is safe here: validation returns
// before any DB access.

func TestAppendCommit_RejectsOverlongDocID(t *testing.T) {
	l := New(nil)
	err := l.AppendCommit(context.Background(), strings.Repeat("x", 256), changelog.Commit{ID: "x"})
	if err == nil {
		t.Fatal("want error for doc_id over 255 chars, got nil")
	}
}

func TestMarkSeen_RejectsOverlongKey(t *testing.T) {
	l := New(nil)
	err := l.MarkSeen(context.Background(), "doc", strings.Repeat("k", 256), changelog.Commit{ID: "x"})
	if err == nil {
		t.Fatal("want error for idempotency_key over 255 chars, got nil")
	}
}

func TestCheckLen_AcceptsBoundary(t *testing.T) {
	if err := checkLen("doc_id", strings.Repeat("x", 255)); err != nil {
		t.Fatalf("255 chars must be allowed: %v", err)
	}
}
