package changelogclickhouse

import (
	"strings"
	"testing"
)

// ClickHouse has no unique constraints; dedup identity lives entirely in the
// ReplacingMergeTree ORDER BY key, reconciled at read time via FINAL. This guards
// that model: changing the engine or the ORDER BY key silently breaks dedup.
func TestDDL_ReplacingMergeTreeDedup(t *testing.T) {
	if !strings.Contains(commitsDDL, "ReplacingMergeTree") {
		t.Error("commits must use ReplacingMergeTree for eventual dedup")
	}
	if !strings.Contains(commitsDDL, "ORDER BY (doc_id, at, id)") {
		t.Error("commits ORDER BY key defines the dedup identity (doc_id, at, id)")
	}
	if !strings.Contains(seenDDL, "ReplacingMergeTree") {
		t.Error("seen must use ReplacingMergeTree")
	}
	if !strings.Contains(seenDDL, "ORDER BY (doc_id, idempotency_key)") {
		t.Error("seen dedup identity is (doc_id, idempotency_key): keys are scoped per document")
	}
}
