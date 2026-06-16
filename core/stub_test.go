package changelog

import (
	"context"
	"sync"
)

// memLog is a minimal in-memory Log for exercising the Recorder in core tests.
// The production in-memory backend lives in adapters/memory; core tests use this
// local fake so the core module depends on no Log implementation.
type memLog struct {
	mu      sync.Mutex
	commits map[string][]Commit
}

func newMemLog() *memLog { return &memLog{commits: map[string][]Commit{}} }

func (m *memLog) AppendCommit(ctx context.Context, docID string, c Commit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.commits[docID] = append(m.commits[docID], c)
	return nil
}

func (m *memLog) Commits(ctx context.Context, docID string, limit int) ([]Commit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	src := m.commits[docID]
	out := make([]Commit, 0, len(src))
	for i := len(src) - 1; i >= 0; i-- {
		out = append(out, src[i])
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

func (m *memLog) Head(ctx context.Context, docID string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	src := m.commits[docID]
	if len(src) == 0 {
		return "", nil
	}
	return src[len(src)-1].ID, nil
}

var _ Log = (*memLog)(nil)
