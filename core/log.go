package changelog

import (
	"context"
)

// Log is the repository — pluggable storage for the per-document commit history,
// the port every backend implements. Each document is its own git branch:
// AppendCommit extends the history, Head is the latest commit, and Commits is
// `git log`. (The Recorder is the porcelain that produces those commits.)
//
// The Log does no concurrency control: AppendCommit stores the commit it is
// given, parent included. Two writers building on the same parent record a fork
// — two honest facts, not an error. Serializing writers, when a deployment
// wants linear history, is the persistence layer's or the producer's job, never
// this interface's.
//
// The memory adapter satisfies the contract with no external dependencies
// (reference/test only); a durable backend is the same contract over real
// storage, shipped as a sibling adapter module with its own driver dependency
// (e.g. adapters/sql). Every implementation must pass the conformance suite.
type Log interface {
	// AppendCommit stores one commit for a document. The commit's Parent is the
	// writer's assertion of the snapshot it built against; the Log stores it
	// verbatim, fork or not.
	AppendCommit(ctx context.Context, docID string, c Commit) error
	// Commits returns a document's commits, newest first (its `git log`).
	// limit <= 0 means all.
	Commits(ctx context.Context, docID string, limit int) ([]Commit, error)
	// Head returns the document's most recent commit ID by arrival order, "" if
	// it has no commits yet. Under forks it is the latest commit, not
	// necessarily the only tip.
	Head(ctx context.Context, docID string) (string, error)
}
