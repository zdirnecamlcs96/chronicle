package chroniclekit

import (
	"context"
	"encoding/json"
	"testing"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
	"github.com/zdirnecamlcs96/chronicle/kit/internal/memlog"
	chronicleschema "github.com/zdirnecamlcs96/chronicle/kit/schema"
)

// bareLog exposes only the mandatory Log contract — no optional capabilities —
// so a Kit over it exercises the no-Annotator paths.
type bareLog struct{ changelog.Log }

// freshNames is the seal-time resolution a caller passes: the external
// referents' names as they are RIGHT NOW, which is the whole point of
// computing the sidecar at write time.
func freshNames() chronicleschema.Option {
	return chronicleschema.WithNames(map[string]string{"u1": "Old Owner", "u2": "New Owner"})
}

// RecordUpdate with WithReadable stores a sidecar keyed by the sealed commit
// ID, rows aligned 1:1 with the commit's Changes, display names frozen from
// the call's WithNames.
func TestReadable_RecordUpdateStoresSidecar(t *testing.T) {
	ctx := context.Background()
	log := memlog.New()
	k := New(log, WithReadable())

	c, err := k.RecordUpdate(ctx, "doc",
		map[string]any{"owner": "u1"}, map[string]any{"owner": "u2"},
		WithActor("a"), WithDiffOptions(freshNames()))
	if err != nil {
		t.Fatalf("RecordUpdate: %v", err)
	}

	rd, err := k.Readables(ctx, "doc", []string{c.ID})
	if err != nil {
		t.Fatalf("Readables: %v", err)
	}
	r, ok := rd[c.ID]
	if !ok || r.Error != "" {
		t.Fatalf("sidecar for %s: %+v ok=%v", c.ID, r, ok)
	}
	if len(r.Rows) != len(c.Changes) {
		t.Fatalf("rows = %d, want %d (ordinal alignment)", len(r.Rows), len(c.Changes))
	}
	row := r.Rows[0]
	if row.Path != c.Changes[0].Path || row.Kind != c.Changes[0].Kind {
		t.Fatalf("row mirrors change: %+v vs %+v", row, c.Changes[0])
	}
	if row.Display == nil || row.Display.From != "Old Owner" || row.Display.To != "New Owner" {
		t.Fatalf("frozen display: %+v", row.Display)
	}
}

// Without WithReadable no sidecar is written, even on a capable backend.
func TestReadable_OffByDefault(t *testing.T) {
	ctx := context.Background()
	log := memlog.New()
	k := New(log)

	c, err := k.RecordUpdate(ctx, "doc", nil, map[string]any{"owner": "u1"})
	if err != nil {
		t.Fatal(err)
	}
	rd, err := k.Readables(ctx, "doc", []string{c.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(rd) != 0 {
		t.Fatalf("sidecar written without WithReadable: %+v", rd)
	}
}

// WithCaptureBaseline seals two commits; both get sidecars.
func TestReadable_BaselineGetsSidecar(t *testing.T) {
	ctx := context.Background()
	log := memlog.New()
	k := New(log, WithReadable())

	before := map[string]any{"owner": "u1"}
	after := map[string]any{"owner": "u2"}
	c, err := k.RecordUpdate(ctx, "doc", before, after,
		WithCaptureBaseline("import", "sys"), WithDiffOptions(freshNames()))
	if err != nil {
		t.Fatal(err)
	}
	if c.Parent == "" {
		t.Fatal("expected delta parented to baseline")
	}
	rd, err := k.Readables(ctx, "doc", []string{c.ID, c.Parent})
	if err != nil {
		t.Fatal(err)
	}
	if len(rd) != 2 {
		t.Fatalf("sidecars = %d, want 2 (baseline + delta): %+v", len(rd), rd)
	}
	// The baseline decorates under the same diff options: its owner-create row
	// carries the seal-time name too.
	base := rd[c.Parent]
	if len(base.Rows) == 0 || base.Rows[0].Display == nil || base.Rows[0].Display.To != "Old Owner" {
		t.Fatalf("baseline display: %+v", base.Rows)
	}
}

// RecordPatch routes through RecordUpdate, so the patch commit gets a sidecar.
func TestReadable_RecordPatchGetsSidecar(t *testing.T) {
	ctx := context.Background()
	log := memlog.New()
	k := New(log, WithReadable())

	if _, err := k.RecordUpdate(ctx, "doc", nil, map[string]any{"owner": "u1"}); err != nil {
		t.Fatal(err)
	}
	c, err := k.RecordPatch(ctx, "doc",
		[]Operation{{Op: "replace", Path: "/owner", Value: json.RawMessage(`"u2"`)}},
		WithDiffOptions(freshNames()))
	if err != nil {
		t.Fatalf("RecordPatch: %v", err)
	}
	rd, err := k.Readables(ctx, "doc", []string{c.ID})
	if err != nil {
		t.Fatal(err)
	}
	r := rd[c.ID]
	if len(r.Rows) != 1 || r.Rows[0].Display == nil || r.Rows[0].Display.To != "New Owner" {
		t.Fatalf("patch sidecar: %+v", r)
	}
}

// Direct RecordChanges has no before-state to decorate against: no sidecar,
// no error stub — absence means "decorate live", exactly like pre-feature
// history.
func TestReadable_RecordChangesSkipped(t *testing.T) {
	ctx := context.Background()
	log := memlog.New()
	k := New(log, WithReadable())

	c, err := k.RecordChanges(ctx, "doc", []changelog.Change{
		{Actor: "a", Path: "owner", Kind: chronicleschema.KindCreate, To: `"u1"`},
	})
	if err != nil {
		t.Fatal(err)
	}
	rd, err := k.Readables(ctx, "doc", []string{c.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(rd) != 0 {
		t.Fatalf("RecordChanges wrote a sidecar: %+v", rd)
	}
}

// A failing sidecar save never fails the commit; the retry lands an error
// stub so readers can acknowledge the gap.
func TestReadable_SaveFailureLandsErrorStub(t *testing.T) {
	ctx := context.Background()
	log := memlog.New()
	k := New(log, WithReadable())
	log.FailNextSaveAnnotations = 1

	c, err := k.RecordUpdate(ctx, "doc", nil, map[string]any{"owner": "u1"})
	if err != nil {
		t.Fatalf("commit must not fail on sidecar error: %v", err)
	}
	rd, err := k.Readables(ctx, "doc", []string{c.ID})
	if err != nil {
		t.Fatal(err)
	}
	r, ok := rd[c.ID]
	if !ok || r.Error == "" || len(r.Rows) != 0 {
		t.Fatalf("error stub: %+v ok=%v", r, ok)
	}
}

// Even the stub write failing leaves the commit successful — the sidecar is
// simply absent.
func TestReadable_DoubleFailureStillCommits(t *testing.T) {
	ctx := context.Background()
	log := memlog.New()
	k := New(log, WithReadable())
	log.FailNextSaveAnnotations = 2

	c, err := k.RecordUpdate(ctx, "doc", nil, map[string]any{"owner": "u1"})
	if err != nil {
		t.Fatalf("commit must not fail on double sidecar failure: %v", err)
	}
	rd, err := k.Readables(ctx, "doc", []string{c.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(rd) != 0 {
		t.Fatalf("expected no sidecar after double failure: %+v", rd)
	}
}

// Readables on a backend without the Annotator capability is nil, nil; and
// WithReadable there is a silent no-op.
func TestReadable_NoCapability(t *testing.T) {
	ctx := context.Background()
	k := New(bareLog{memlog.New()}, WithReadable())

	c, err := k.RecordUpdate(ctx, "doc", nil, map[string]any{"owner": "u1"})
	if err != nil {
		t.Fatalf("RecordUpdate on bare backend: %v", err)
	}
	rd, err := k.Readables(ctx, "doc", []string{c.ID})
	if err != nil || rd != nil {
		t.Fatalf("Readables without capability: %v, %v — want nil, nil", rd, err)
	}
}

// Kit.Explain overlays seal-time names from stored sidecars (stored wins) and
// decorates commits without one live (pre-feature history heals with
// read-time names).
func TestKitExplain_StoredNamesWin(t *testing.T) {
	ctx := context.Background()
	log := memlog.New()

	// Commit 1: sealed before the feature was enabled — no sidecar.
	k0 := New(log)
	if _, err := k0.RecordUpdate(ctx, "doc", nil, map[string]any{"owner": "u1"}); err != nil {
		t.Fatal(err)
	}
	// Commit 2: sealed with the feature on and seal-time names in hand.
	k := New(log, WithReadable())
	if _, err := k.RecordUpdate(ctx, "doc",
		map[string]any{"owner": "u1"}, map[string]any{"owner": "u2"},
		WithDiffOptions(freshNames())); err != nil {
		t.Fatal(err)
	}

	// Read later, when the caller's dictionary has moved on.
	commits, rows, err := k.Explain(ctx, "doc",
		chronicleschema.WithNames(map[string]string{"u1": "Latest 1", "u2": "Latest 2"}))
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if len(commits) != 2 || len(rows) != 2 {
		t.Fatalf("chain: %d commits, %d row groups", len(commits), len(rows))
	}
	// Pre-feature commit: live decoration, read-time names.
	if d := rows[0][0].Display; d == nil || d.To != "Latest 1" {
		t.Fatalf("pre-feature commit display: %+v, want live 'Latest 1'", d)
	}
	// Sidecar'd commit: seal-time names win.
	if d := rows[1][0].Display; d == nil || d.From != "Old Owner" || d.To != "New Owner" {
		t.Fatalf("sidecar'd commit display: %+v, want frozen Old/New Owner", d)
	}
}

// A sidecar payload that cannot align with the sealed changes (foreign or
// stale writer) is skipped: live rendering stands.
func TestKitExplain_MisalignedSidecarFallsBackLive(t *testing.T) {
	ctx := context.Background()
	log := memlog.New()
	k := New(log, WithReadable())

	c, err := k.RecordUpdate(ctx, "doc", nil, map[string]any{"owner": "u1"},
		WithDiffOptions(freshNames()))
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the stored payload: row count no longer matches the commit.
	if err := log.SaveAnnotation(ctx, changelog.Annotation{
		DocID: "doc", CommitID: c.ID, Data: []byte(`{"rows":[{"path":"x","kind":"put"},{"path":"y","kind":"put"}]}`),
	}); err != nil {
		t.Fatal(err)
	}

	_, rows, err := k.Explain(ctx, "doc",
		chronicleschema.WithNames(map[string]string{"u1": "Live"}))
	if err != nil {
		t.Fatal(err)
	}
	if d := rows[0][0].Display; d == nil || d.To != "Live" {
		t.Fatalf("misaligned sidecar must fall back live: %+v", d)
	}
}
