package chroniclekit

import (
	"context"
	"errors"
	"reflect"
	"testing"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
	"github.com/zdirnecamlcs96/chronicle/kit/internal/memlog"
)

// The onboarding case: a document that existed before recording began gets TWO
// commits — a baseline root holding the full before-state as creates, then the
// caller's delta parented to it. The returned commit is always the delta, and
// the chain can reconstruct the pre-existing state again.
func TestKit_CaptureBaseline_OnboardsPreexistingDoc(t *testing.T) {
	ctx := context.Background()
	inner := memlog.New()
	k := New(inner)

	before := map[string]any{"status": "draft", "qty": 1}
	after := map[string]any{"status": "sent", "qty": 1}
	delta, err := k.RecordUpdate(ctx, "doc", before, after,
		WithCaptureBaseline("captured pre-existing state", "importer"), WithActor("alice"))
	if err != nil {
		t.Fatalf("RecordUpdate: %v", err)
	}

	cs, err := inner.Commits(ctx, "doc", 0) // newest first
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 {
		t.Fatalf("stored %d commits, want 2 (baseline + delta)", len(cs))
	}
	base := cs[1]
	if base.Parent != "" {
		t.Fatalf("baseline.Parent = %q, want \"\" (the root)", base.Parent)
	}
	if base.Message != "captured pre-existing state" {
		t.Fatalf("baseline.Message = %q", base.Message)
	}
	if !reflect.DeepEqual(base.Authors, []string{"importer"}) {
		t.Fatalf("baseline.Authors = %v, want [importer]", base.Authors)
	}
	for _, ch := range base.Changes {
		if ch.Kind != KindCreate {
			t.Fatalf("baseline change %s kind = %q, want create", ch.Path, ch.Kind)
		}
	}
	if cs[0].ID != delta.ID {
		t.Fatalf("returned commit must be the delta: got %s, head is %s", delta.ID, cs[0].ID)
	}
	if delta.Parent != base.ID {
		t.Fatalf("delta.Parent = %q, want the baseline %q", delta.Parent, base.ID)
	}

	atRoot, err := k.StateAt(ctx, "doc", base.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(atRoot, norm(t, before)) {
		t.Fatalf("state at baseline = %#v, want the before document %#v", atRoot, norm(t, before))
	}
	state, err := k.State(ctx, "doc")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state, norm(t, after)) {
		t.Fatalf("head state = %#v, want %#v", state, norm(t, after))
	}
}

// nil before is a create — the diff already roots the chain at the full
// document, so no baseline.
func TestKit_CaptureBaseline_NilBefore_NoBaseline(t *testing.T) {
	ctx := context.Background()
	inner := memlog.New()
	k := New(inner)

	c, err := k.RecordUpdate(ctx, "doc", nil, map[string]any{"status": "open"},
		WithCaptureBaseline("captured", "importer"))
	if err != nil {
		t.Fatal(err)
	}
	cs, err := inner.Commits(ctx, "doc", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || c.Parent != "" {
		t.Fatalf("want a single root commit, got %d commits, parent %q", len(cs), c.Parent)
	}
}

// A document that already has a chain never gets a baseline — the option is a
// no-op past the first write.
func TestKit_CaptureBaseline_ExistingChain_NoBaseline(t *testing.T) {
	ctx := context.Background()
	inner := memlog.New()
	k := New(inner)

	doc0 := map[string]any{"qty": 1}
	doc1 := map[string]any{"qty": 2}
	seed, err := k.RecordUpdate(ctx, "doc", nil, doc0)
	if err != nil {
		t.Fatal(err)
	}
	c, err := k.RecordUpdate(ctx, "doc", doc0, doc1,
		WithCaptureBaseline("captured", "importer"))
	if err != nil {
		t.Fatal(err)
	}
	cs, err := inner.Commits(ctx, "doc", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 {
		t.Fatalf("stored %d commits, want 2 (seed + delta, no baseline)", len(cs))
	}
	if c.Parent != seed.ID {
		t.Fatalf("delta.Parent = %q, want the existing head %q", c.Parent, seed.ID)
	}
}

// A no-op write must not onboard a document: empty delta returns
// ErrEmptyChanges exactly as today, and seals nothing.
func TestKit_CaptureBaseline_NoopUpdate_NoOnboard(t *testing.T) {
	ctx := context.Background()
	inner := memlog.New()
	k := New(inner)

	doc := map[string]any{"a": 1}
	_, err := k.RecordUpdate(ctx, "doc", doc, doc,
		WithCaptureBaseline("captured", "importer"))
	if !errors.Is(err, changelog.ErrEmptyChanges) {
		t.Fatalf("want ErrEmptyChanges for no-op update, got %v", err)
	}
	cs, err := inner.Commits(ctx, "doc", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 0 {
		t.Fatalf("stored %d commits, want 0 — a no-op must not onboard", len(cs))
	}
}

// The crash-heal path: a baseline-only chain (delta lost between the two
// seals) heals on the next write — it sees the head and seals just its delta.
func TestKit_CaptureBaseline_HealsBaselineOnlyChain(t *testing.T) {
	ctx := context.Background()
	inner := memlog.New()
	k := New(inner)

	before := map[string]any{"status": "draft"}
	after := map[string]any{"status": "sent"}
	// Seed just the baseline: the commit shape a crash between the seals leaves.
	seedBase, err := k.RecordUpdate(ctx, "doc", nil, before,
		WithMessage("captured"), WithActor("importer"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := k.RecordUpdate(ctx, "doc", before, after,
		WithCaptureBaseline("captured", "importer"))
	if err != nil {
		t.Fatal(err)
	}
	cs, err := inner.Commits(ctx, "doc", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 {
		t.Fatalf("stored %d commits, want 2 — retry must not seal a second baseline", len(cs))
	}
	if c.Parent != seedBase.ID {
		t.Fatalf("delta.Parent = %q, want the seeded baseline %q", c.Parent, seedBase.ID)
	}
}

// Empty-object before: the baseline diff yields nothing, so it is skipped
// silently and the delta roots the chain as today.
func TestKit_CaptureBaseline_EmptyObjectBefore_SkipsBaseline(t *testing.T) {
	ctx := context.Background()
	inner := memlog.New()
	k := New(inner)

	c, err := k.RecordUpdate(ctx, "doc", map[string]any{}, map[string]any{"a": 1},
		WithCaptureBaseline("captured", "importer"))
	if err != nil {
		t.Fatal(err)
	}
	cs, err := inner.Commits(ctx, "doc", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || c.Parent != "" {
		t.Fatalf("want a single root delta, got %d commits, parent %q", len(cs), c.Parent)
	}
}

// The baseline is diffed under the same options as the delta, so keyed arrays
// record with the element identity the delta uses: the delta pairs elements by
// key against the baseline, and the root reconstructs the full before document.
func TestKit_CaptureBaseline_KeyedArrays_SameIdentity(t *testing.T) {
	ctx := context.Background()
	inner := memlog.New()
	k := New(inner)

	before := lines(el("P1", 1), el("P2", 2))
	after := lines(el("P2", 9), el("P1", 1)) // reordered + one keyed edit
	delta, err := k.RecordUpdate(ctx, "doc", before, after,
		WithDiffOptions(linesKey), WithCaptureBaseline("captured", "importer"))
	if err != nil {
		t.Fatal(err)
	}
	cs, err := inner.Commits(ctx, "doc", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 {
		t.Fatalf("stored %d commits, want 2", len(cs))
	}
	// Keyed pairing against the baseline: one qty change, no churn from the
	// reorder — the identity the baseline recorded matches the delta's.
	if len(delta.Changes) != 1 || delta.Changes[0].Path != "lines.1.qty" || delta.Changes[0].To != "9" {
		t.Fatalf("want single keyed change lines.1.qty->9, got %+v", delta.Changes)
	}
	atRoot, err := k.StateAt(ctx, "doc", cs[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(atRoot, norm(t, before)) {
		t.Fatalf("state at baseline = %#v, want %#v", atRoot, norm(t, before))
	}
}

// Hard regression guard: the same call WITHOUT the option behaves exactly as
// today — the chain roots at the first delta.
func TestKit_CaptureBaseline_WithoutOption_Unchanged(t *testing.T) {
	ctx := context.Background()
	inner := memlog.New()
	k := New(inner)

	before := map[string]any{"status": "draft", "qty": 1}
	after := map[string]any{"status": "sent", "qty": 1}
	c, err := k.RecordUpdate(ctx, "doc", before, after)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := inner.Commits(ctx, "doc", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || c.Parent != "" {
		t.Fatalf("without the option the delta must root the chain: %d commits, parent %q", len(cs), c.Parent)
	}
}
