package conformance

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/zdirnecamlcs96/chronicle/core"
)

// NewLog returns a fresh, empty Log plus a teardown func. The suite calls it
// once per subtest. Backends with external state (e.g. a SQL database) truncate
// their tables / close their handle in teardown; in-memory backends return a
// no-op func.
type NewLog func(t *testing.T) (changelog.Log, func())

// RunLogConformance runs the mandatory changelog.Log contract against the
// backend produced by newLog. Every Log implementation must pass it — it is the
// executable specification of the port.
func RunLogConformance(t *testing.T, newLog NewLog) {
	t.Helper()

	t.Run("EmptyHead", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		h, err := log.Head(context.Background(), "missing")
		if err != nil {
			t.Fatalf("Head: %v", err)
		}
		if h != "" {
			t.Fatalf("Head of unknown doc = %q, want \"\"", h)
		}
	})

	t.Run("EmptyCommits", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		cs, err := log.Commits(context.Background(), "missing", 0)
		if err != nil {
			t.Fatalf("Commits: %v", err)
		}
		if len(cs) != 0 {
			t.Fatalf("Commits of unknown doc = %d rows, want 0", len(cs))
		}
	})

	t.Run("AppendThenHead", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		cs := sealN(t, log, "doc", 2)
		h, err := log.Head(context.Background(), "doc")
		if err != nil {
			t.Fatal(err)
		}
		if h != cs[1].ID {
			t.Fatalf("Head = %q, want last id %q", h, cs[1].ID)
		}
	})

	t.Run("ParentChaining", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		cs := sealN(t, log, "doc", 2)
		if cs[0].Parent != "" {
			t.Fatalf("root parent = %q, want \"\"", cs[0].Parent)
		}
		if cs[1].Parent != cs[0].ID {
			t.Fatalf("chain broken: c2.Parent = %q, want %q", cs[1].Parent, cs[0].ID)
		}
	})

	t.Run("CommitsNewestFirst", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		cs := sealN(t, log, "doc", 3)
		got, err := log.Commits(context.Background(), "doc", 0)
		if err != nil {
			t.Fatal(err)
		}
		assertIDs(t, got, cs[2].ID, cs[1].ID, cs[0].ID)
	})

	t.Run("CommitsLimit", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		cs := sealN(t, log, "doc", 3)
		ctx := context.Background()
		two, err := log.Commits(ctx, "doc", 2)
		if err != nil {
			t.Fatal(err)
		}
		assertIDs(t, two, cs[2].ID, cs[1].ID)
		if all, _ := log.Commits(ctx, "doc", 0); len(all) != 3 {
			t.Fatalf("limit<=0 = %d, want 3 (all)", len(all))
		}
		if all, _ := log.Commits(ctx, "doc", 99); len(all) != 3 {
			t.Fatalf("limit>count = %d, want 3", len(all))
		}
	})

	t.Run("PerDocIsolation", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		sealN(t, log, "docA", 2)
		b := sealN(t, log, "docB", 1)
		ctx := context.Background()
		if ca, _ := log.Commits(ctx, "docA", 0); len(ca) != 2 {
			t.Fatalf("docA = %d commits, want 2", len(ca))
		}
		cb, _ := log.Commits(ctx, "docB", 0)
		if len(cb) != 1 || cb[0].ID != b[0].ID {
			t.Fatalf("docB isolation broken: %v", ids(cb))
		}
	})

	t.Run("ContextCancellation", func(t *testing.T) {
		log, done := newLog(t)
		defer done()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := log.AppendCommit(ctx, "doc", changelog.Commit{ID: "x"}); !errors.Is(err, context.Canceled) {
			t.Fatalf("AppendCommit: want context.Canceled, got %v", err)
		}
		if _, err := log.Commits(ctx, "doc", 0); !errors.Is(err, context.Canceled) {
			t.Fatalf("Commits: want context.Canceled, got %v", err)
		}
		if _, err := log.Head(ctx, "doc"); !errors.Is(err, context.Canceled) {
			t.Fatalf("Head: want context.Canceled, got %v", err)
		}
	})
}

// RunSerializableAppend is the stronger, opt-in contract: under concurrent seals
// to the SAME document, the stored commits must form a single linear chain — no
// two commits share a parent, exactly one root. A naive backend whose
// AppendCommit blindly stores whatever parent the Recorder computed (e.g.
// MemoryLog) forks here and must NOT run this; a backend that serializes appends
// (e.g. the SQL adapter via FOR UPDATE + a unique (doc,parent) constraint)
// passes. Conflicting seals are expected to fail and are ignored — the assertion
// is about the final stored state, not that every goroutine succeeds.
func RunSerializableAppend(t *testing.T, newLog NewLog) {
	t.Helper()
	log, done := newLog(t)
	defer done()
	ctx := context.Background()

	const goroutines = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := changelog.NewRecorder("doc", log)
			rec.Append(changelog.Change{Actor: "a", Path: "p", Kind: "put", To: fmt.Sprintf("v%d", i)})
			<-start
			_, _ = rec.Commit(ctx) // parent conflicts are expected; ignore them
		}(i)
	}
	close(start)
	wg.Wait()

	commits, err := log.Commits(ctx, "doc", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) == 0 {
		t.Fatal("no commits landed under concurrent append")
	}
	byID := make(map[string]bool, len(commits))
	for _, c := range commits {
		byID[c.ID] = true
	}
	parents := make(map[string]bool, len(commits))
	roots := 0
	for _, c := range commits {
		if parents[c.Parent] {
			t.Fatalf("fork: two commits share parent %q", c.Parent)
		}
		parents[c.Parent] = true
		if c.Parent == "" {
			roots++
			continue
		}
		if !byID[c.Parent] {
			t.Fatalf("dangling parent %q points to no stored commit", c.Parent)
		}
	}
	if roots != 1 {
		t.Fatalf("want exactly one root (parent==\"\"), got %d", roots)
	}
}

// sealN seals n chained commits into log via a Recorder with a MONOTONIC clock,
// so every commit gets a distinct, increasing timestamp. This keeps the suite
// portable: backends that order by seq (SQL) AND backends that order by
// timestamp (ClickHouse, columnar) both return a deterministic newest-first.
// Commits chain through the Log's Head, so a backend with a unique (doc,parent)
// constraint accepts them (each parent is distinct).
func sealN(t *testing.T, log changelog.Log, docID string, n int) []changelog.Commit {
	t.Helper()
	ctx := context.Background()
	var tick int64
	monotonic := func() time.Time { tick++; return time.Unix(tick, 0).UTC() }
	rec := changelog.NewRecorder(docID, log).WithClock(monotonic)
	out := make([]changelog.Commit, 0, n)
	for i := 0; i < n; i++ {
		rec.Append(changelog.Change{Actor: "a", Path: "p", Kind: "put", To: fmt.Sprintf("v%d", i)})
		c, err := rec.Commit(ctx)
		if err != nil {
			t.Fatalf("seal commit %d on %q: %v", i, docID, err)
		}
		out = append(out, c)
	}
	return out
}

func assertIDs(t *testing.T, got []changelog.Commit, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d commits %v, want %d", len(got), ids(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i] {
			t.Fatalf("commit[%d].ID = %q, want %q (got %v)", i, got[i].ID, want[i], ids(got))
		}
	}
}

func ids(cs []changelog.Commit) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}
