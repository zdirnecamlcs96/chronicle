package changelog

import "context"

// This file defines OPTIONAL capability interfaces a Log backend MAY implement.
// They are not part of the core Log contract (which stays AppendCommit/Commits/
// Head). Consumers type-assert a Log for these and fall back to their own
// bookkeeping when a backend (such as the memory adapter) does not implement
// them. A durable backend (e.g. adapters/sql) implements them natively so that
// cross-document queries and producer idempotency survive a restart.

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
	// Seen returns the commit a key previously sealed; ok is false if unseen.
	Seen(ctx context.Context, key string) (c Commit, ok bool, err error)
	// MarkSeen records that key sealed commit c for document docID. First writer
	// wins: it is a no-op if the key already exists.
	MarkSeen(ctx context.Context, key, docID string, c Commit) error
}
