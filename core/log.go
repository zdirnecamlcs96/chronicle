package changelog

import (
	"context"
	"errors"
)

// ErrParentConflict is returned by a Log's AppendCommit when the commit's Parent
// no longer matches the document's current Head — git's non-fast-forward: another
// commit landed between the Recorder reading Head and this append. The caller
// re-reads Head and re-seals (the Recorder re-chains and re-hashes), so the retry
// is correct. The memory adapter never returns it — it stores whatever parent it
// is given, so it can fork; durable backends that serialize concurrent
// same-document appends (e.g. adapters/sql) enforce it.
var ErrParentConflict = errors.New("changelog: commit parent does not match current head")

// Log is the repository — pluggable storage for the per-document commit history,
// the port every backend implements. Each document is its own git branch:
// AppendCommit extends the chain, Head is the branch tip, and Commits is `git log`.
// (The Recorder is the porcelain that produces those commits.)
//
// The memory adapter satisfies the contract with no external dependencies
// (reference/test only); a durable backend is the same contract over real
// storage, shipped as a sibling adapter module with its own driver dependency
// (e.g. adapters/sql). Every implementation must pass the conformance suite.
type Log interface {
	// AppendCommit stores one commit for a document, extending its chain.
	AppendCommit(ctx context.Context, docID string, c Commit) error
	// Commits returns a document's commits, newest first (its `git log`).
	// limit <= 0 means all.
	Commits(ctx context.Context, docID string, limit int) ([]Commit, error)
	// Head returns the document's tip commit ID, "" if it has no commits yet.
	Head(ctx context.Context, docID string) (string, error)
}
