package changelogclickhouse

import (
	"database/sql"
	"strings"
	"testing"
)

// ClickHouse has no unique constraints; dedup identity lives entirely in the
// ReplacingMergeTree ORDER BY key, reconciled at read time via FINAL. This guards
// that model: changing the engine or the ORDER BY key silently breaks dedup.
func TestDDL_ReplacingMergeTreeDedup(t *testing.T) {
	commits := commitsDDL("commits")
	seen := seenDDL("seen")
	snapshots := snapshotsDDL("snapshots")
	if !strings.Contains(commits, "ReplacingMergeTree") {
		t.Error("commits must use ReplacingMergeTree for eventual dedup")
	}
	if !strings.Contains(commits, "ORDER BY (doc_id, at, id)") {
		t.Error("commits ORDER BY key defines the dedup identity (doc_id, at, id)")
	}
	if !strings.Contains(seen, "ReplacingMergeTree") {
		t.Error("seen must use ReplacingMergeTree")
	}
	if !strings.Contains(seen, "ORDER BY (doc_id, idempotency_key)") {
		t.Error("seen dedup identity is (doc_id, idempotency_key): keys are scoped per document")
	}
	if !strings.Contains(snapshots, "ReplacingMergeTree(at)") {
		t.Error("snapshots must version on at for latest-wins dedup")
	}
	if !strings.Contains(snapshots, "ORDER BY (doc_id)") {
		t.Error("snapshots ORDER BY key defines the dedup identity (doc_id): one snapshot per document")
	}
}

// TestDDL_UsesGivenTableName proves the DDL builders interpolate the caller's
// table name rather than a hardcoded default.
func TestDDL_UsesGivenTableName(t *testing.T) {
	if !strings.Contains(commitsDDL("my_commits"), "CREATE TABLE IF NOT EXISTS my_commits") {
		t.Error("commitsDDL must create the given table name")
	}
	if !strings.Contains(seenDDL("my_seen"), "CREATE TABLE IF NOT EXISTS my_seen") {
		t.Error("seenDDL must create the given table name")
	}
	if !strings.Contains(snapshotsDDL("my_snapshots"), "CREATE TABLE IF NOT EXISTS my_snapshots") {
		t.Error("snapshotsDDL must create the given table name")
	}
	if !strings.Contains(annotationsDDL("my_annotations"), "CREATE TABLE IF NOT EXISTS my_annotations") {
		t.Error("annotationsDDL must create the given table name")
	}
}

// TestNew_ValidatesPrefix proves the table prefix is required, must be a
// plain identifier once a trailing underscore is stripped, and derives the
// four table names as <prefix>_changelog_<table>.
func TestNew_ValidatesPrefix(t *testing.T) {
	db, err := sql.Open("clickhouse", "clickhouse://127.0.0.1:9000/default")
	if err != nil {
		t.Fatalf("open driver: %v", err)
	}
	defer db.Close()

	tests := []struct {
		name    string
		prefix  string
		wantErr string
	}{
		{"empty", "", "required"},
		{"bad identifier", "bad-prefix;", "invalid table prefix"},
		{"valid", "myapp", ""},
		{"valid trailing underscore", "myapp_", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, err := New(db, tt.prefix)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("New(%q) = %v, want no error", tt.prefix, err)
				}
				want := tables{
					Commits:     "myapp_changelog_commits",
					Seen:        "myapp_changelog_seen",
					Snapshots:   "myapp_changelog_snapshots",
					Annotations: "myapp_changelog_annotations",
				}
				if l.t != want {
					t.Fatalf("New(%q).t = %+v, want %+v", tt.prefix, l.t, want)
				}
				return
			}
			if err == nil {
				t.Fatalf("New(%q) = nil error, want error mentioning %q", tt.prefix, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("New(%q) error = %q, want to mention %q", tt.prefix, err, tt.wantErr)
			}
		})
	}
}
