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
	// ErrBrokenChain means a commit's Parent is not the previous commit's ID.
	ErrBrokenChain = errors.New("changelog: commit parent does not match previous commit id")
	// ErrFork means two commits share a parent (or two roots exist) — the
	// history is not a single linear chain.
	ErrFork = errors.New("changelog: two commits share a parent")
)

// VerifyChain checks that commits — AS RETURNED BY Log.Commits, newest first —
// form one tamper-free linear chain: exactly one root, each commit's Parent
// equal to its predecessor's ID, no two commits sharing a parent, and every ID
// equal to the recomputed content hash of its (Parent, Message, Changes).
// An empty history is valid.
//
// The recompute is sound because adapters return Changes as Go structs
// (re-marshaled in fixed field order), not raw stored column text.
func VerifyChain(commits []Commit) error {
	if len(commits) == 0 {
		return nil
	}
	seenParent := make(map[string]bool, len(commits))
	prev := "" // the previous commit's ID, walking oldest → newest
	for i := len(commits) - 1; i >= 0; i-- {
		c := commits[i]
		pos := len(commits) - 1 - i // 0 = root
		if seenParent[c.Parent] {
			return fmt.Errorf("%w: commit %d (%s) repeats parent %q", ErrFork, pos, c.ID, c.Parent)
		}
		seenParent[c.Parent] = true
		if c.Parent != prev {
			return fmt.Errorf("%w: commit %d (%s) has parent %q, want %q", ErrBrokenChain, pos, c.ID, c.Parent, prev)
		}
		id, err := computeID(c.Parent, c.Message, c.Changes)
		if err != nil {
			return err
		}
		if id != c.ID {
			return fmt.Errorf("%w: commit %d claims %s", ErrHashMismatch, pos, c.ID)
		}
		prev = c.ID
	}
	return nil
}

// Verify fetches docID's full history from log and runs VerifyChain over it.
func Verify(ctx context.Context, log Log, docID string) error {
	commits, err := log.Commits(ctx, docID, 0)
	if err != nil {
		return err
	}
	return VerifyChain(commits)
}
