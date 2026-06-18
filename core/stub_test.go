package changelog

import (
	"context"
	"sort"
	"sync"
)

// memLog is a minimal in-memory Log for exercising the Recorder in core tests.
// The production in-memory backend lives in adapters/memory; core tests use this
// local fake so the core module depends on no Log implementation.
type memLog struct {
	mu      sync.Mutex
	commits map[string][]Commit
	seen    map[[2]string]Commit // {docID, key} -> commit, mirrors the Deduper contract
}

func newMemLog() *memLog {
	return &memLog{commits: map[string][]Commit{}, seen: map[[2]string]Commit{}}
}

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

func (m *memLog) AllCommits(ctx context.Context, limit int) ([]DocCommit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []DocCommit{}
	for _, id := range m.docIDs() {
		src := m.commits[id]
		for i := len(src) - 1; i >= 0; i-- {
			out = append(out, DocCommit{DocID: id, Commit: src[i]})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Commit.At.After(out[j].Commit.At) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memLog) FindByID(ctx context.Context, commitID string) (DocCommit, bool, error) {
	if err := ctx.Err(); err != nil {
		return DocCommit{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range m.docIDs() {
		for _, c := range m.commits[id] {
			if c.ID == commitID {
				return DocCommit{DocID: id, Commit: c}, true, nil
			}
		}
	}
	return DocCommit{}, false, nil
}

// docIDs returns document ids in ascending order; caller holds m.mu.
func (m *memLog) docIDs() []string {
	ids := make([]string, 0, len(m.commits))
	for d := range m.commits {
		ids = append(ids, d)
	}
	sort.Strings(ids)
	return ids
}

func (m *memLog) Seen(ctx context.Context, docID, key string) (Commit, bool, error) {
	if err := ctx.Err(); err != nil {
		return Commit{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.seen[[2]string{docID, key}]
	return c, ok, nil
}

func (m *memLog) MarkSeen(ctx context.Context, docID, key string, c Commit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := [2]string{docID, key}
	if _, ok := m.seen[k]; !ok {
		m.seen[k] = c
	}
	return nil
}

var (
	_ Log     = (*memLog)(nil)
	_ Indexer = (*memLog)(nil)
	_ Deduper = (*memLog)(nil)
)
