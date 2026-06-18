package changelog

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrNothingToCommit is returned by Recorder.Commit when nothing is staged.
var ErrNothingToCommit = errors.New("changelog: nothing to commit")

// Recorder is the porcelain — the staging area plus `git commit`, bound to one
// document: Append stages Changes, Commit seals them into a hash-chained Commit.
// (The Log is the repository where the sealed Commits live.) A Recorder is safe
// for concurrent use.
type Recorder struct {
	docID string
	log   Log
	now   func() time.Time

	mu      sync.Mutex
	pending []Change
}

// NewRecorder returns a Recorder that records the history of docID into log.
// The Recorder uses time.Now().UTC for change and commit timestamps; use
// WithClock to inject a deterministic clock for tests or replay.
func NewRecorder(docID string, log Log) *Recorder {
	return &Recorder{docID: docID, log: log, now: func() time.Time { return time.Now().UTC() }}
}

// WithClock replaces the Recorder's clock and returns the Recorder so callers
// can chain it onto NewRecorder. A nil now resets to time.Now().UTC.
func (r *Recorder) WithClock(now func() time.Time) *Recorder {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	r.mu.Lock()
	r.now = now
	r.mu.Unlock()
	return r
}

// Append stages c (like `git add`). The Recorder stamps c.At from its own clock,
// overwriting any value the caller provided, so it remains the single source of
// time.
func (r *Recorder) Append(c Change) {
	r.mu.Lock()
	c.At = r.now()
	r.pending = append(r.pending, c)
	r.mu.Unlock()
}

// Pending returns a copy of the staged, not-yet-committed Changes (like `git status`).
func (r *Recorder) Pending() []Change {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Change(nil), r.pending...)
}

// commitOpts holds the optional knobs applied to a single Commit call.
type commitOpts struct{ message string }

// CommitOption configures one call to Recorder.Commit. Pass via Commit's
// variadic opts argument. Options apply in order.
type CommitOption func(*commitOpts)

// WithMessage annotates the commit (`git commit -m`). The message is part of the
// commit hash, so editing it after the fact breaks the chain.
func WithMessage(s string) CommitOption {
	return func(o *commitOpts) { o.message = s }
}

// Commit seals every staged Change into a new Commit, hash-chained onto the
// document's current Head, and appends it to the Log — `git commit`. It returns
// ErrNothingToCommit when nothing is staged. On any error the staged Changes are
// restored, so nothing is lost. Options (e.g. WithMessage) are variadic and
// additive — existing callers `rec.Commit(ctx)` continue to work.
func (r *Recorder) Commit(ctx context.Context, opts ...CommitOption) (Commit, error) {
	var co commitOpts
	for _, opt := range opts {
		opt(&co)
	}

	r.mu.Lock()
	pending := r.pending
	r.pending = nil
	r.mu.Unlock()

	if len(pending) == 0 {
		return Commit{}, ErrNothingToCommit
	}

	parent, err := r.log.Head(ctx, r.docID)
	if err != nil {
		r.restore(pending)
		return Commit{}, err
	}
	id, err := computeID(parent, co.message, pending)
	if err != nil {
		r.restore(pending)
		return Commit{}, err
	}
	r.mu.Lock()
	now := r.now()
	r.mu.Unlock()
	c := Commit{
		ID:      id,
		Parent:  parent,
		At:      now,
		Authors: distinctAuthors(pending),
		Message: co.message,
		Changes: pending,
	}
	if err := r.log.AppendCommit(ctx, r.docID, c); err != nil {
		r.restore(pending)
		return Commit{}, err
	}
	return c, nil
}

// restore puts a failed Commit's changes back at the front of the buffer.
func (r *Recorder) restore(changes []Change) {
	r.mu.Lock()
	r.pending = append(changes, r.pending...)
	r.mu.Unlock()
}
