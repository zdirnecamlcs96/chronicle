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
	// ErrAuthorsMismatch means a commit's Authors is not the sorted, distinct
	// actor set of its Changes. Authors is derived metadata and NOT hashed, so
	// this recomputation is the only check that catches editing it after sealing.
	ErrAuthorsMismatch = errors.New("changelog: commit authors do not match its changes' actors")
)

// VerifyChain checks that commits — AS RETURNED BY Log.Commits, newest first —
// form one tamper-free linear chain: exactly one root, each commit's Parent
// equal to its predecessor's ID, no two commits sharing a parent, every ID
// equal to the recomputed content hash of its (Parent, Message, Changes), and
// every Authors equal to the recomputed distinct actor set of its Changes
// (Authors is derived, not hashed — recomputation is what protects it).
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
		if !authorsMatch(c.Authors, c.Changes) {
			return fmt.Errorf("%w: commit %d (%s)", ErrAuthorsMismatch, pos, c.ID)
		}
		prev = c.ID
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
func Verify(ctx context.Context, log Log, docID string) error {
	commits, err := log.Commits(ctx, docID, 0)
	if err != nil {
		return err
	}
	return VerifyChain(commits)
}

// VerifyChainAfter checks that tail — the commits strictly after a trusted
// anchor commit, OLDEST first (as TailReader.CommitsAfter returns them) —
// extends anchorID as one tamper-free linear chain. anchorID "" means tail is
// the full history from the root. An empty tail is valid.
//
// Because each commit's ID is the content hash of (Parent, Message, Changes)
// and Parent is inside that hash, a verified anchor transitively attests every
// commit behind it: re-checking the prefix adds nothing. The flip side is the
// trust contract — tampering AT OR BEFORE the anchor is invisible here; run a
// full VerifyChain when the anchor's provenance is itself in doubt.
func VerifyChainAfter(anchorID string, tail []Commit) error {
	seenParent := make(map[string]bool, len(tail))
	prev := anchorID
	for pos, c := range tail {
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
		if !authorsMatch(c.Authors, c.Changes) {
			return fmt.Errorf("%w: commit %d (%s)", ErrAuthorsMismatch, pos, c.ID)
		}
		prev = c.ID
	}
	return nil
}

// VerifyAfter incrementally verifies docID: it fetches only the commits after
// anchorID via the log's TailReader and runs VerifyChainAfter — O(commits
// since the anchor) instead of O(all commits). It returns the new verified
// head (anchorID itself when the tail is empty), which the caller persists as
// the anchor for the next run. anchorID "" verifies from the root. A log
// without TailReader, or an anchorID no longer on the document
// (ErrNoSuchCommit — the history was rewritten under the anchor), is an error.
func VerifyAfter(ctx context.Context, log Log, docID, anchorID string) (head string, err error) {
	tr, ok := log.(TailReader)
	if !ok {
		return "", errors.New("changelog: incremental verify requires a Log with TailReader")
	}
	tail, err := tr.CommitsAfter(ctx, docID, anchorID, 0)
	if err != nil {
		return "", err
	}
	if err := VerifyChainAfter(anchorID, tail); err != nil {
		return "", err
	}
	if len(tail) == 0 {
		return anchorID, nil
	}
	return tail[len(tail)-1].ID, nil
}
