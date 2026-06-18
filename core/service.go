package changelog

import (
	"context"
	"errors"
)

// ErrEmptyChanges is returned by Service.Seal when no changes are supplied.
var ErrEmptyChanges = errors.New("changelog: seal requires at least one change")

// maxSealAttempts bounds the retry loop when a durable backend reports
// ErrParentConflict (a concurrent same-document append raced in).
const maxSealAttempts = 5

// Service is the in-process changelog facade — the operations a server needs,
// over any Log, with NO transport and no net/http dependency. Construct it once
// with NewService and call it directly in a Go server; an HTTP layer (an
// http.Handler you write) is one optional adapter on top, not a requirement.
//
// It adds the two things the per-document Log port lacks: cross-document queries
// (AllCommits, Get) and producer idempotency (Seal + WithIdempotencyKey).
type Service interface {
	// Seal stages changes for docID and commits them as a single Commit, retrying
	// transparently on ErrParentConflict. WithIdempotencyKey makes a replay return
	// the already-sealed commit. Returns ErrEmptyChanges if changes is empty.
	Seal(ctx context.Context, docID string, changes []Change, message string, opts ...SealOption) (Commit, error)
	// Commits returns a document's commits, newest first. limit <= 0 means all.
	Commits(ctx context.Context, docID string, limit int) ([]Commit, error)
	// AllCommits returns commits across all documents, newest first.
	AllCommits(ctx context.Context, limit int) ([]DocCommit, error)
	// Get returns the commit with the given id and its document; ok is false if
	// no such commit exists.
	Get(ctx context.Context, commitID string) (dc DocCommit, ok bool, err error)
}

type sealConfig struct{ idempotencyKey string }

// SealOption configures a single Seal call.
type SealOption func(*sealConfig)

// WithIdempotencyKey makes Seal a dedup-safe replay: a later Seal carrying the
// same key returns the already-sealed commit instead of sealing a duplicate, so
// an at-least-once delivery retry lands exactly one commit.
func WithIdempotencyKey(key string) SealOption {
	return func(c *sealConfig) { c.idempotencyKey = key }
}

// service is the default Service implementation: a thin facade that holds no
// storage state of its own. It delegates cross-document queries to the backend's
// Indexer and producer idempotency to its Deduper (both detected through any
// Unwrap() wrappers). A backend without an Indexer cannot answer cross-document
// queries (AllCommits is empty, Get finds nothing); a backend without a Deduper
// does not dedup (Seal stays at-least-once, never replaying another document's
// commit). The reference adapters/memory implements both.
type service struct {
	log   Log
	index Indexer
	idem  Deduper
}

// NewService wraps a Log with cross-document queries and producer idempotency,
// detecting the backend's optional Indexer/Deduper through any Unwrap() chain.
// The service keeps no fallback state: cross-document queries require an Indexer
// backend and idempotency requires a Deduper. adapters/memory and the durable
// adapters (e.g. adapters/sql) implement both.
func NewService(log Log) Service {
	s := &service{log: log}
	for l := log; l != nil; {
		if s.index == nil {
			if idx, ok := l.(Indexer); ok {
				s.index = idx
			}
		}
		if s.idem == nil {
			if d, ok := l.(Deduper); ok {
				s.idem = d
			}
		}
		if s.index != nil && s.idem != nil {
			break
		}
		u, ok := l.(interface{ Unwrap() Log })
		if !ok {
			break
		}
		l = u.Unwrap()
	}
	return s
}

func (s *service) Seal(ctx context.Context, docID string, changes []Change, message string, opts ...SealOption) (Commit, error) {
	if len(changes) == 0 {
		return Commit{}, ErrEmptyChanges
	}
	var cfg sealConfig
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.idempotencyKey != "" {
		if prior, ok, err := s.lookupSeen(ctx, docID, cfg.idempotencyKey); err != nil {
			return Commit{}, err
		} else if ok {
			return prior, nil
		}
	}
	rec := NewRecorder(docID, s.log)
	for _, c := range changes {
		rec.Append(c)
	}
	var cOpts []CommitOption
	if message != "" {
		cOpts = append(cOpts, WithMessage(message))
	}
	var c Commit
	var err error
	for attempt := 0; attempt < maxSealAttempts; attempt++ {
		c, err = rec.Commit(ctx, cOpts...)
		if !errors.Is(err, ErrParentConflict) {
			break
		}
		// A concurrent same-doc append landed; the Recorder restored the pending
		// changes, so the next iteration re-reads Head and re-chains/re-hashes.
	}
	if err != nil {
		return Commit{}, err
	}
	if cfg.idempotencyKey != "" {
		// Best-effort: a failed MarkSeen degrades to at-least-once; the commit is
		// already durable.
		_ = s.recordSeen(ctx, docID, cfg.idempotencyKey, c)
	}
	return c, nil
}

func (s *service) lookupSeen(ctx context.Context, docID, key string) (Commit, bool, error) {
	if s.idem != nil {
		return s.idem.Seen(ctx, docID, key)
	}
	return Commit{}, false, nil
}

func (s *service) recordSeen(ctx context.Context, docID, key string, c Commit) error {
	if s.idem != nil {
		return s.idem.MarkSeen(ctx, docID, key, c)
	}
	return nil
}

func (s *service) Commits(ctx context.Context, docID string, limit int) ([]Commit, error) {
	return s.log.Commits(ctx, docID, limit)
}

func (s *service) AllCommits(ctx context.Context, limit int) ([]DocCommit, error) {
	if s.index != nil {
		return s.index.AllCommits(ctx, limit)
	}
	return []DocCommit{}, nil
}

func (s *service) Get(ctx context.Context, commitID string) (DocCommit, bool, error) {
	if s.index != nil {
		return s.index.FindByID(ctx, commitID)
	}
	return DocCommit{}, false, nil
}
