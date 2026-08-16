package chroniclekit

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/zdirnecamlcs96/chronicle/kit/internal/memlog"
	chronicleschema "github.com/zdirnecamlcs96/chronicle/kit/schema"
)

// A faithful actual — recorded and reconstructed state agree — must Reconcile
// to no drift.
func TestReconcile_FaithfulActual(t *testing.T) {
	ctx := context.Background()
	k := New(memlog.New())
	after := map[string]any{"status": "open", "qty": 5}
	if _, err := k.RecordUpdate(ctx, "doc", nil, after); err != nil {
		t.Fatalf("RecordUpdate: %v", err)
	}

	d, err := k.Reconcile(ctx, "doc", after)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !d.Empty() {
		t.Fatalf("want no drift, got %+v", d.Changes)
	}
}

// A bypassed write — the database holds a field the log never recorded —
// drifts as a create: From empty (the log has nothing there), To the
// database's value.
func TestReconcile_BypassedWrite(t *testing.T) {
	ctx := context.Background()
	k := New(memlog.New())
	if _, err := k.RecordUpdate(ctx, "doc", nil, map[string]any{"status": "open"}); err != nil {
		t.Fatalf("RecordUpdate: %v", err)
	}

	actual := map[string]any{"status": "open", "flag": true}
	d, err := k.Reconcile(ctx, "doc", actual)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(d.Changes) != 1 {
		t.Fatalf("want one drift entry, got %+v", d.Changes)
	}
	ch := d.Changes[0]
	if ch.Path != "flag" || ch.From != "" || ch.To != "true" {
		t.Fatalf("want flag create (From \"\", To true), got %+v", ch)
	}
}

// A phantom record — the log recorded a field the database lacks — drifts as
// a delete: From the logged value, To empty (the row has nothing there).
func TestReconcile_PhantomRecord(t *testing.T) {
	ctx := context.Background()
	k := New(memlog.New())
	if _, err := k.RecordUpdate(ctx, "doc", nil, map[string]any{"status": "open", "qty": 5}); err != nil {
		t.Fatalf("RecordUpdate: %v", err)
	}

	actual := map[string]any{"status": "open"}
	d, err := k.Reconcile(ctx, "doc", actual)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(d.Changes) != 1 {
		t.Fatalf("want one drift entry, got %+v", d.Changes)
	}
	ch := d.Changes[0]
	if ch.Path != "qty" || ch.From != "5" || ch.To != "" {
		t.Fatalf("want qty delete (From 5, To \"\"), got %+v", ch)
	}
}

// A WithIgnoredFields bare-name entry excludes that field from drift, at any
// depth — the read-side semantics invert here versus Explain: the field is
// excluded from comparison outright, not merely flagged.
func TestReconcile_IgnoredFieldExcluded(t *testing.T) {
	ctx := context.Background()
	k := New(memlog.New())
	if _, err := k.RecordUpdate(ctx, "doc", nil, map[string]any{"status": "open", "updated_at": "t0"}); err != nil {
		t.Fatalf("RecordUpdate: %v", err)
	}

	actual := map[string]any{"status": "open", "updated_at": "t1"}
	d, err := k.Reconcile(ctx, "doc", actual, chronicleschema.WithIgnoredFields("updated_at"))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !d.Empty() {
		t.Fatalf("want ignored field excluded from drift, got %+v", d.Changes)
	}
}

// A WithIgnoredFields dotted schema path excludes only that nested field's
// subtree — a sibling field at the same depth still drifts.
func TestReconcile_IgnoredNestedPathExcluded(t *testing.T) {
	ctx := context.Background()
	k := New(memlog.New())
	before := map[string]any{"meta": map[string]any{"rev": 1, "name": "a"}}
	if _, err := k.RecordUpdate(ctx, "doc", nil, before); err != nil {
		t.Fatalf("RecordUpdate: %v", err)
	}

	actual := map[string]any{"meta": map[string]any{"rev": 99, "name": "a"}}
	d, err := k.Reconcile(ctx, "doc", actual, chronicleschema.WithIgnoredFields("meta.rev"))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !d.Empty() {
		t.Fatalf("want meta.rev excluded, got %+v", d.Changes)
	}

	// A change to the sibling field is not covered by the "meta.rev" entry and
	// must still be reported.
	actual2 := map[string]any{"meta": map[string]any{"rev": 1, "name": "b"}}
	d2, err := k.Reconcile(ctx, "doc", actual2, chronicleschema.WithIgnoredFields("meta.rev"))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if d2.Empty() || d2.Changes[0].Path != "meta.name" {
		t.Fatalf("want meta.name drift, got %+v", d2.Changes)
	}
}

// Reconcile works over a non-object root too: once a root-level replace flips
// the document to an array, a faithful actual against it still folds to no
// drift.
func TestReconcile_ArrayRoot(t *testing.T) {
	ctx := context.Background()
	k := New(memlog.New())
	obj := map[string]any{"a": 1}
	arr := []any{1, 2, 3}
	if _, err := k.RecordUpdate(ctx, "doc", nil, obj); err != nil {
		t.Fatalf("RecordUpdate create: %v", err)
	}
	if _, err := k.RecordUpdate(ctx, "doc", obj, arr); err != nil {
		t.Fatalf("RecordUpdate flip: %v", err)
	}

	d, err := k.Reconcile(ctx, "doc", arr)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !d.Empty() {
		t.Fatalf("want no drift, got %+v", d.Changes)
	}
}

// ReconcileSweep reports ids on only one side: LogOnly for a committed
// document absent from dbDocIDs, DBOnly for a live row the log never saw.
func TestReconcileSweep(t *testing.T) {
	ctx := context.Background()
	k := NewWithService(memlog.NewService())
	if _, err := k.RecordUpdate(ctx, "a", nil, map[string]any{"x": 1}); err != nil {
		t.Fatalf("RecordUpdate a: %v", err)
	}
	if _, err := k.RecordUpdate(ctx, "b", nil, map[string]any{"x": 1}); err != nil {
		t.Fatalf("RecordUpdate b: %v", err)
	}

	report, err := k.ReconcileSweep(ctx, []string{"b", "c"})
	if err != nil {
		t.Fatalf("ReconcileSweep: %v", err)
	}
	if !reflect.DeepEqual(report.LogOnly, []string{"a"}) {
		t.Fatalf("LogOnly = %v, want [a]", report.LogOnly)
	}
	if !reflect.DeepEqual(report.DBOnly, []string{"c"}) {
		t.Fatalf("DBOnly = %v, want [c]", report.DBOnly)
	}
}

// A backend without Indexer cannot enumerate the log's documents at all —
// ReconcileSweep must fail loudly rather than silently reporting no drift.
func TestReconcileSweep_RequiresIndexer(t *testing.T) {
	ctx := context.Background()
	k := New(bareLog{memlog.New()})

	if _, err := k.ReconcileSweep(ctx, []string{"a"}); !errors.Is(err, ErrNoIndexer) {
		t.Fatalf("want ErrNoIndexer, got %v", err)
	}
}
