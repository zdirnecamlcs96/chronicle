package chronicleexplain

import (
	"encoding/json"
	"reflect"
	"testing"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
	"github.com/zdirnecamlcs96/chronicle/kit/internal/docmodel"
	chronicleschema "github.com/zdirnecamlcs96/chronicle/kit/schema"
)

// ExplainChanges must be the single-commit form of Explain: decorating each
// commit's changes against the replayed parent state yields exactly the rows
// the full-chain replay yields.
func TestExplainChanges_ParityWithExplain(t *testing.T) {
	opts := []chronicleschema.Option{
		chronicleschema.WithArrayKeys(map[string]string{"lines": "id"}),
		chronicleschema.WithIdentityFields("id"),
		chronicleschema.WithNames(map[string]string{`"u1"`: "External"}),
	}
	commits := commitsFor(t, []any{
		map[string]any{"title": "Doc", "owner": "u1"},
		map[string]any{"title": "Doc2", "owner": "u1",
			"lines": []any{map[string]any{"id": "t1", "name": "Fragile", "qty": float64(1)}}},
		map[string]any{"title": "Doc2", "owner": "u1",
			"lines": []any{map[string]any{"id": "t1", "name": "Fragile", "qty": float64(2)}}},
	}, opts...)

	full, err := Explain(commits, opts...)
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}

	var cur any
	for i, c := range commits {
		rows, err := ExplainChanges(cur, c.Changes, opts...)
		if err != nil {
			t.Fatalf("ExplainChanges commit %d: %v", i, err)
		}
		if !reflect.DeepEqual(rows, full[i]) {
			t.Fatalf("commit %d rows diverge:\n one-shot: %+v\n replayed: %+v", i, rows, full[i])
		}
		for _, ch := range c.Changes {
			if cur, err = docmodel.Apply(cur, ch); err != nil {
				t.Fatalf("apply: %v", err)
			}
		}
	}
}

// ExplainChanges must not mutate the caller's before state — RecordUpdate
// hands it the live document.
func TestExplainChanges_DoesNotMutateBefore(t *testing.T) {
	before := map[string]any{"title": "Doc", "lines": []any{map[string]any{"id": "t1", "qty": float64(1)}}}
	want := map[string]any{"title": "Doc", "lines": []any{map[string]any{"id": "t1", "qty": float64(1)}}}
	commits := commitsFor(t, []any{
		before,
		map[string]any{"title": "Doc2", "lines": []any{map[string]any{"id": "t1", "qty": float64(2)}}},
	})
	if _, err := ExplainChanges(before, commits[1].Changes); err != nil {
		t.Fatalf("ExplainChanges: %v", err)
	}
	if !reflect.DeepEqual(before, want) {
		t.Fatalf("before mutated: %+v", before)
	}
}

// ReadableOf projects decorated rows to the stored lite form, 1:1 by ordinal,
// and the envelope round-trips through JSON with either rows or an error.
func TestReadableOf_ProjectionAndEnvelope(t *testing.T) {
	rows := []Explained{{
		Change:  changelog.Change{Path: "lines.0.qty", Kind: "update", From: "1", To: "2"},
		Field:   []string{"Fragile", "Qty"},
		Element: &Element{Trail: []string{"Lines"}, ID: "t1", Name: "Fragile"},
		Display: &Display{From: "Old", To: "New"},
	}}
	r := ReadableOf(rows)
	if len(r.Rows) != 1 || r.Error != "" {
		t.Fatalf("ReadableOf: %+v", r)
	}
	row := r.Rows[0]
	if row.Path != "lines.0.qty" || row.Kind != "update" || row.From != "1" || row.To != "2" {
		t.Fatalf("row change fields: %+v", row)
	}
	if !reflect.DeepEqual(row.Field, rows[0].Field) ||
		!reflect.DeepEqual(row.Element, rows[0].Element) ||
		!reflect.DeepEqual(row.Display, rows[0].Display) {
		t.Fatalf("row decoration fields: %+v", row)
	}

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var back Readable
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, r) {
		t.Fatalf("envelope round-trip: %+v != %+v", back, r)
	}

	stub, _ := json.Marshal(Readable{Error: "boom"})
	if string(stub) != `{"error":"boom"}` {
		t.Fatalf("error stub: %s", stub)
	}
}
