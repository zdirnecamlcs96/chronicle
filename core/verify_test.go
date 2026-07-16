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
		{"BrokenParentLink", func(cs []Commit) []Commit {
			// Point the tip at a non-existent parent; its ID still matches its
			// content (recomputed), so the linkage check must catch it.
			cs[0].Parent = "0000000000000000000000000000000000000000000000000000000000000000"
			id, _ := computeID(cs[0].Parent, cs[0].Message, cs[0].Changes)
			cs[0].ID = id
			return cs
		}, ErrBrokenChain},
		{"Fork", func(cs []Commit) []Commit {
			// A second child of cs[2] (the root), content-valid.
			forged := Commit{Parent: cs[2].ID, Message: "fork", Changes: []Change{{Actor: "x", Path: "p", Kind: "put", To: "f"}}}
			forged.ID, _ = computeID(forged.Parent, forged.Message, forged.Changes)
			return append([]Commit{forged}, cs...)
		}, ErrFork},
		{"TwoRoots", func(cs []Commit) []Commit {
			root2 := Commit{Parent: "", Message: "second root", Changes: []Change{{Actor: "x", Path: "p", Kind: "put", To: "r"}}}
			root2.ID, _ = computeID(root2.Parent, root2.Message, root2.Changes)
			return append([]Commit{root2}, cs...)
		}, ErrFork},
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
