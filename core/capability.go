package changelog

import "context"

// This file defines OPTIONAL capability interfaces a Log backend MAY implement.
// They are not part of the core Log contract (which stays AppendCommit/Commits/
// Head). The Service (NewService) type-asserts a Log for these and delegates to
// them; it keeps no fallback of its own, so a backend that implements neither
// simply has no cross-document queries and no dedup. Every shipped adapter
// implements both — adapters/sql and adapters/clickhouse durably (across a
// restart), adapters/memory in memory.

// DocCommit pairs a Commit with the document it belongs to, for cross-document
// query results. The per-document Log methods omit the docID because the caller
// already supplied it; cross-document results must self-identify.
type DocCommit struct {
	DocID  string
	Commit Commit
}

// Indexer is an optional capability for backends that answer cross-document
// queries natively — `git log --all` over every document, and looking a commit up
// by its hash regardless of which document it belongs to (`git show <id>`).
// Without it, a consumer must keep its own index of the documents it has seen.
type Indexer interface {
	// AllCommits returns commits across all documents, newest first
	// (`git log --all`). limit <= 0 means all.
	AllCommits(ctx context.Context, limit int) ([]DocCommit, error)
	// FindByID returns the commit with the given id and its document, anywhere in
	// the store; ok is false if no such commit exists.
	FindByID(ctx context.Context, commitID string) (dc DocCommit, ok bool, err error)
}

// Deduper is an optional capability that makes producer idempotency durable: a
// delivery retry carrying a previously seen key returns the original commit
// instead of sealing a duplicate, even across a restart.
type Deduper interface {
	// Seen returns the commit (docID, key) previously sealed; ok is false if
	// unseen. Keys are scoped per document: the same key on a different docID is a
	// distinct delivery, so a replay never returns another document's commit.
	Seen(ctx context.Context, docID, key string) (c Commit, ok bool, err error)
	// MarkSeen records that key sealed commit c for document docID. First writer
	// wins: it is a no-op if (docID, key) already exists.
	MarkSeen(ctx context.Context, docID, key string, c Commit) error
}
