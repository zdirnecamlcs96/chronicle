package changelog

import (
	"context"
	"errors"
)

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

// ErrNoSuchCommit is returned by TailReader.CommitsAfter when afterID does not
// exist on the document.
var ErrNoSuchCommit = errors.New("changelog: no such commit")

// TailReader is an optional capability for backends that can read a document's
// chain from a cursor — the commits strictly AFTER a known commit, in replay
// order. It is what lets a reader (e.g. chroniclekit's snapshot fast path)
// resume from a checkpoint instead of refetching the whole history.
type TailReader interface {
	// CommitsAfter returns docID's commits strictly after afterID, OLDEST first
	// (chronological — the replay order, opposite of Commits). afterID "" means
	// from the root. limit <= 0 means all. An afterID not on the document
	// returns ErrNoSuchCommit.
	CommitsAfter(ctx context.Context, docID, afterID string, limit int) ([]Commit, error)
}

// Snapshot is an opaque materialization of a document as of CommitID. Core
// never interprets State; the layer that wrote it (e.g. chroniclekit) does.
type Snapshot struct {
	DocID    string
	CommitID string
	State    []byte
}

// Snapshotter is an optional capability: one cached snapshot per document,
// latest write wins. It is a pure cache — deleting stored snapshots is always
// safe, a reader falls back to full replay.
type Snapshotter interface {
	// SaveSnapshot stores s, replacing any prior snapshot for s.DocID.
	SaveSnapshot(ctx context.Context, s Snapshot) error
	// LoadSnapshot returns the stored snapshot for docID; ok is false if none.
	LoadSnapshot(ctx context.Context, docID string) (s Snapshot, ok bool, err error)
}

// Annotation is an opaque per-commit sidecar, stored OUTSIDE the hash seal.
// Core never interprets Data; the layer that wrote it (e.g. chroniclekit's
// readable display projection) does. It is non-authoritative by construction —
// the sealed chain stays the record — so deleting stored annotations is always
// safe: a reader falls back to deriving what it can live.
type Annotation struct {
	DocID    string
	CommitID string
	Data     []byte
}

// Annotator is an optional capability: at most one annotation per commit,
// latest write wins.
type Annotator interface {
	// SaveAnnotation stores a, replacing any prior annotation for
	// (a.DocID, a.CommitID).
	SaveAnnotation(ctx context.Context, a Annotation) error
	// LoadAnnotations returns docID's annotations for the given commit ids,
	// keyed by commit id; ids without one are simply absent. Empty ids means an
	// empty map, never an error.
	LoadAnnotations(ctx context.Context, docID string, commitIDs []string) (map[string][]byte, error)
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
