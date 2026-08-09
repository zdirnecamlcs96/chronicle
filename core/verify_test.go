package changelog

import (
	"context"
	"errors"
	"testing"
	"time"
)

// sealChain seals n commits into a fresh memLog and returns the log plus the
// stored history as Log.Commits returns it (newest first).
func sealChain(t *testing.T, n int) (*memLog, []Commit) {
	t.Helper()
	log := newMemLog()
	var tick int64
	rec := NewRecorder("doc", log).WithClock(func() time.Time { tick++; return time.Unix(tick, 0).UTC() })
	for i := 0; i < n; i++ {
		rec.Append(Change{Actor: "a", Path: "p", Kind: "put", To: "v"})
		if _, err := rec.Commit(context.Background(), WithMessage("m")); err != nil {
			t.Fatal(err)
		}
	}
	commits, err := log.Commits(context.Background(), "doc", 0)
	if err != nil {
		t.Fatal(err)
	}
	return log, commits
}

func TestVerifyChain(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(cs []Commit) []Commit // takes a valid newest-first chain of 3
		wantErr error                      // nil means valid
	}{
		{"Valid", func(cs []Commit) []Commit { return cs }, nil},
		{"Empty", func(cs []Commit) []Commit { return nil }, nil},
		{"TamperedMessage", func(cs []Commit) []Commit {
			cs[1].Message = "edited after the fact"
			return cs
		}, ErrHashMismatch},
		{"TamperedChange", func(cs []Commit) []Commit {
			cs[0].Changes[0].To = "forged"
			return cs
		}, ErrHashMismatch},
		{"TamperedAuthors", func(cs []Commit) []Commit {
			// Authors is not hashed, so only the recomputation from Changes
			// can catch an after-the-fact edit.
			cs[1].Authors = []string{"mallory"}
			return cs
		}, ErrAuthorsMismatch},
		{"MissingParent", func(cs []Commit) []Commit {
			// Point the tip at a non-existent parent; its ID still matches its
			// content (recomputed), so the ancestry check must catch it.
			cs[0].Parent = "0000000000000000000000000000000000000000000000000000000000000000"
			id, _ := computeID(cs[0].Parent, cs[0].Message, cs[0].Changes)
			cs[0].ID = id
			return cs
		}, ErrMissingParent},
		{"ForkIsLegal", func(cs []Commit) []Commit {
			// A second child of cs[2] (the root), content-valid — two writers
			// raced; both facts are recorded, neither is corruption.
			sibling := Commit{Parent: cs[2].ID, Message: "fork", Authors: []string{"x"}, Changes: []Change{{Actor: "x", Path: "p", Kind: "put", To: "f"}}}
			sibling.ID, _ = computeID(sibling.Parent, sibling.Message, sibling.Changes)
			return append([]Commit{sibling}, cs...)
		}, nil},
		{"TwoRootsAreLegal", func(cs []Commit) []Commit {
			root2 := Commit{Parent: "", Message: "second root", Authors: []string{"x"}, Changes: []Change{{Actor: "x", Path: "p", Kind: "put", To: "r"}}}
			root2.ID, _ = computeID(root2.Parent, root2.Message, root2.Changes)
			return append([]Commit{root2}, cs...)
		}, nil},
		{"TamperInFork", func(cs []Commit) []Commit {
			// A fork is still content-checked like any other commit.
			sibling := Commit{Parent: cs[2].ID, Message: "fork", Authors: []string{"x"}, Changes: []Change{{Actor: "x", Path: "p", Kind: "put", To: "f"}}}
			sibling.ID, _ = computeID(sibling.Parent, sibling.Message, sibling.Changes)
			sibling.Message = "edited after the fact"
			return append([]Commit{sibling}, cs...)
		}, ErrHashMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, cs := sealChain(t, 3)
			err := VerifyChain(tt.mutate(cs))
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("VerifyChain: %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("VerifyChain: %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// chrono reverses a newest-first history into replay order (oldest first),
// as TailReader.CommitsAfter returns it.
func chrono(cs []Commit) []Commit {
	out := make([]Commit, len(cs))
	for i, c := range cs {
		out[len(cs)-1-i] = c
	}
	return out
}

func TestVerifyCommits(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(tail []Commit) []Commit // takes commits 3..4 of a chain of 5, oldest first
		wantErr error
	}{
		{"Valid", func(tail []Commit) []Commit { return tail }, nil},
		{"Empty", func(tail []Commit) []Commit { return nil }, nil},
		{"Tampered", func(tail []Commit) []Commit {
			tail[1].Message = "edited after the fact"
			return tail
		}, ErrHashMismatch},
		{"TamperedAuthors", func(tail []Commit) []Commit {
			tail[1].Authors = append(tail[1].Authors, "mallory")
			return tail
		}, ErrAuthorsMismatch},
		{"ForkedBatchIsLegal", func(tail []Commit) []Commit {
			// A sibling of tail[1] — content-valid, parent outside any anchor.
			// VerifyCommits is a content check only; ancestry is VerifyChain's job.
			sibling := Commit{Parent: tail[1].Parent, Message: "fork", Authors: []string{"x"}, Changes: []Change{{Actor: "x", Path: "p", Kind: "put", To: "f"}}}
			sibling.ID, _ = computeID(sibling.Parent, sibling.Message, sibling.Changes)
			return append(tail, sibling)
		}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, cs := sealChain(t, 5)
			all := chrono(cs)
			err := VerifyCommits(tt.mutate(all[3:]))
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("VerifyCommits: %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("VerifyCommits: %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestVerifyAfter(t *testing.T) {
	ctx := context.Background()
	log, cs := sealChain(t, 5)
	all := chrono(cs)
	head := all[4].ID

	// Full check from the root; returned head becomes the next anchor.
	got, err := VerifyAfter(ctx, log, "doc", "")
	if err != nil || got != head {
		t.Fatalf("VerifyAfter(root) = %q, %v; want %q, nil", got, err, head)
	}
	// Incremental from a mid-chain anchor.
	if got, err = VerifyAfter(ctx, log, "doc", all[2].ID); err != nil || got != head {
		t.Fatalf("VerifyAfter(anchor) = %q, %v; want %q, nil", got, err, head)
	}
	// Caught up: empty tail returns the anchor itself.
	if got, err = VerifyAfter(ctx, log, "doc", head); err != nil || got != head {
		t.Fatalf("VerifyAfter(head) = %q, %v; want %q, nil", got, err, head)
	}
	// Anchor not on the document.
	if _, err = VerifyAfter(ctx, log, "doc", "no-such-id"); !errors.Is(err, ErrNoSuchCommit) {
		t.Fatalf("unknown anchor: %v, want ErrNoSuchCommit", err)
	}

	// Tampering AFTER the anchor is detected; BEFORE the anchor is not (that is
	// the documented trust contract — content behind the cursor is not refetched).
	log.mu.Lock()
	log.commits["doc"][3].Message = "tampered tail"
	log.mu.Unlock()
	if _, err = VerifyAfter(ctx, log, "doc", all[2].ID); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("tampered tail: %v, want ErrHashMismatch", err)
	}
	log.mu.Lock()
	log.commits["doc"][3].Message = "m" // restore
	log.commits["doc"][1].Message = "tampered prefix"
	log.mu.Unlock()
	if _, err = VerifyAfter(ctx, log, "doc", all[2].ID); err != nil {
		t.Fatalf("pre-anchor tamper must be invisible to incremental verify: %v", err)
	}
}

// tailless wraps a Log to hide every optional capability.
type tailless struct{ Log }

func TestVerifyAfter_RequiresTailReader(t *testing.T) {
	log, _ := sealChain(t, 1)
	if _, err := VerifyAfter(context.Background(), tailless{log}, "doc", ""); err == nil {
		t.Fatal("want error for a Log without TailReader")
	}
}

func TestVerify_FetchesAndChecks(t *testing.T) {
	log, _ := sealChain(t, 2)
	if err := Verify(context.Background(), log, "doc"); err != nil {
		t.Fatalf("valid chain: %v", err)
	}
	// Tamper in place: memLog hands back its stored slice contents by copy on
	// Commits, so corrupt the stored struct directly.
	log.mu.Lock()
	log.commits["doc"][0].Message = "tampered"
	log.mu.Unlock()
	if err := Verify(context.Background(), log, "doc"); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("tampered chain: %v, want ErrHashMismatch", err)
	}
	// Empty doc verifies clean.
	if err := Verify(context.Background(), log, "no-such-doc"); err != nil {
		t.Fatalf("empty doc: %v", err)
	}
}
