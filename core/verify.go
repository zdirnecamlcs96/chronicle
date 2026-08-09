package changelog

import (
	"context"
	"errors"
	"fmt"
)

// Verification errors. VerifyChain wraps them with the offending commit's
// position and ID, so match with errors.Is.
var (
	// ErrHashMismatch means a commit's ID is not the content hash of its
	// (Parent, Message, Changes) — the stored content was altered after sealing.
	ErrHashMismatch = errors.New("changelog: commit id does not match its content hash")
	// ErrMissingParent means a commit's Parent names a commit that is not in the
	// history — the base it claims to build on was never stored (or was removed).
	ErrMissingParent = errors.New("changelog: commit parent is not in the history")
	// ErrAuthorsMismatch means a commit's Authors is not the sorted, distinct
	// actor set of its Changes. Authors is derived metadata and NOT hashed, so
	// this recomputation is the only check that catches editing it after sealing.
	ErrAuthorsMismatch = errors.New("changelog: commit authors do not match its changes' actors")
)

// VerifyChain checks that commits — AS RETURNED BY Log.Commits, newest first —
// form a tamper-free ancestry: every ID equal to the recomputed content hash of
// its (Parent, Message, Changes), every Authors equal to the recomputed distinct
// actor set of its Changes (Authors is derived, not hashed — recomputation is
// what protects it), and every Parent either "" (a root) or the ID of a commit
// in the history. Forks — commits sharing a parent — and multiple roots are
// legal recorded facts, not corruption; cycles are impossible because a parent
// is inside its child's content hash. A linear chain passes unchanged. An empty
// history is valid.
//
// The recompute is sound because adapters return Changes as Go structs
// (re-marshaled in fixed field order), not raw stored column text.
func VerifyChain(commits []Commit) error {
	if len(commits) == 0 {
		return nil
	}
	ids := make(map[string]bool, len(commits))
	for _, c := range commits {
		ids[c.ID] = true
	}
	for i := len(commits) - 1; i >= 0; i-- {
		c := commits[i]
		pos := len(commits) - 1 - i // 0 = oldest
		if c.Parent != "" && !ids[c.Parent] {
			return fmt.Errorf("%w: commit %d (%s) claims parent %q", ErrMissingParent, pos, c.ID, c.Parent)
		}
		id, err := computeID(c.Parent, c.Message, c.Changes)
		if err != nil {
			return err
		}
		if id != c.ID {
			return fmt.Errorf("%w: commit %d claims %s", ErrHashMismatch, pos, c.ID)
		}
		if !authorsMatch(c.Authors, c.Changes) {
			return fmt.Errorf("%w: commit %d (%s)", ErrAuthorsMismatch, pos, c.ID)
		}
	}
	return nil
}

// authorsMatch reports whether stored is exactly distinctAuthors(changes) —
// the sorted, de-duplicated actor set the Recorder derives at seal time.
func authorsMatch(stored []string, changes []Change) bool {
	want := distinctAuthors(changes)
	if len(stored) != len(want) {
		return false
	}
	for i := range want {
		if stored[i] != want[i] {
			return false
		}
	}
	return true
}

// Verify fetches docID's full history from log and runs VerifyChain over it.
// It is the ancestry authority: only the full history can decide whether every
// parent exists.
func Verify(ctx context.Context, log Log, docID string) error {
	commits, err := log.Commits(ctx, docID, 0)
	if err != nil {
		return err
	}
	return VerifyChain(commits)
}

// VerifyCommits content-checks a batch of commits in isolation: every ID equal
// to the recomputed content hash of its (Parent, Message, Changes), every
// Authors equal to the recomputed distinct actor set. It does NOT check that
// parents exist — a fork's parent legitimately lies outside any batch — so it
// cannot detect a commit whose claimed base was never stored. Run a full
// VerifyChain for that. An empty batch is valid.
func VerifyCommits(commits []Commit) error {
	for pos, c := range commits {
		id, err := computeID(c.Parent, c.Message, c.Changes)
		if err != nil {
			return err
		}
		if id != c.ID {
			return fmt.Errorf("%w: commit %d claims %s", ErrHashMismatch, pos, c.ID)
		}
		if !authorsMatch(c.Authors, c.Changes) {
			return fmt.Errorf("%w: commit %d (%s)", ErrAuthorsMismatch, pos, c.ID)
		}
	}
	return nil
}

// VerifyAfter incrementally content-checks docID: it fetches only the commits
// after anchorID via the log's TailReader and runs VerifyCommits — O(commits
// since the anchor) instead of O(all commits). It returns the new cursor (the
// last tail commit's ID, or anchorID itself when the tail is empty), which the
// caller persists for the next run. anchorID "" checks from the root.
//
// The trust contract: each commit's ID hash-covers its Parent, so content
// tampering in the tail is caught here — but whether every parent EXISTS is an
// ancestry question only the full history can answer. Run Verify when the
// document's ancestry is in doubt. A log without TailReader, or an anchorID no
// longer on the document (ErrNoSuchCommit), is an error.
func VerifyAfter(ctx context.Context, log Log, docID, anchorID string) (head string, err error) {
	tr, ok := log.(TailReader)
	if !ok {
		return "", errors.New("changelog: incremental verify requires a Log with TailReader")
	}
	tail, err := tr.CommitsAfter(ctx, docID, anchorID, 0)
	if err != nil {
		return "", err
	}
	if err := VerifyCommits(tail); err != nil {
		return "", err
	}
	if len(tail) == 0 {
		return anchorID, nil
	}
	return tail[len(tail)-1].ID, nil
}
