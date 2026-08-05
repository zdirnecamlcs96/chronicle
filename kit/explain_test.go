package chroniclekit

import (
	"reflect"
	"testing"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
)

// commitsFor diffs each successive state pair into one commit, oldest first,
// starting from nil — the shape Explain expects (chain from root).
func commitsFor(t *testing.T, states []any, opts ...DiffOption) []changelog.Commit {
	t.Helper()
	var commits []changelog.Commit
	prev := any(nil)
	for _, s := range states {
		changes, err := Diff(prev, s, opts...)
		if err != nil {
			t.Fatalf("diff: %v", err)
		}
		commits = append(commits, changelog.Commit{Changes: changes})
		prev = s
	}
	return commits
}

func TestExplain_FieldTrails(t *testing.T) {
	commits := commitsFor(t, []any{
		map[string]any{"status": "open", "shipping": map[string]any{"unit_price": 1}},
		map[string]any{"status": "paid", "shipping": map[string]any{"unit_price": 2}},
	})
	got, err := Explain(commits)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want per-commit rows, got %d", len(got))
	}
	// Rows align 1:1 with each commit's changes, embedded Change intact.
	for i, c := range commits {
		if len(got[i]) != len(c.Changes) {
			t.Fatalf("commit %d: rows misaligned", i)
		}
		for j, row := range got[i] {
			if !reflect.DeepEqual(row.Change, c.Changes[j]) {
				t.Fatalf("embedded Change altered: %+v vs %+v", row.Change, c.Changes[j])
			}
		}
	}
	byPath := map[string]Explained{}
	for _, rows := range got {
		for _, r := range rows {
			byPath[r.Path] = r
		}
	}
	if f := byPath["status"].Field; !reflect.DeepEqual(f, []string{"Status"}) {
		t.Fatalf("document-level scalar wants Field [Status], got %v", f)
	}
	if byPath["status"].Element != nil {
		t.Fatalf("document-level scalar must have nil Element")
	}
	if f := byPath["shipping.unit_price"].Field; !reflect.DeepEqual(f, []string{"Shipping", "Unit Price"}) {
		t.Fatalf("nested field wants humanized trail, got %v", f)
	}
}

func TestExplain_PositionalIndexVerbatim(t *testing.T) {
	commits := commitsFor(t, []any{
		map[string]any{"tags": []any{"a", "b"}},
		map[string]any{"tags": []any{"a", "c"}},
	})
	got, err := Explain(commits)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	last := got[len(got)-1]
	if len(last) != 1 || !reflect.DeepEqual(last[0].Field, []string{"Tags", "1"}) {
		t.Fatalf("positional index must appear verbatim, got %+v", last)
	}
}

func TestExplain_LabelResolver(t *testing.T) {
	resolver := func(path []string) (string, bool) {
		if len(path) == 1 && path[0] == "status" {
			return "order.status", true
		}
		return "", false
	}
	commits := commitsFor(t, []any{
		map[string]any{"status": "open", "qty": 1},
		map[string]any{"status": "paid", "qty": 2},
	})
	got, err := Explain(commits, WithLabels(resolver))
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	byPath := map[string]Explained{}
	for _, rows := range got {
		for _, r := range rows {
			byPath[r.Path] = r
		}
	}
	if !reflect.DeepEqual(byPath["status"].Field, []string{"order.status"}) {
		t.Fatalf("resolver label must be used verbatim, got %v", byPath["status"].Field)
	}
	if !reflect.DeepEqual(byPath["qty"].Field, []string{"Qty"}) {
		t.Fatalf("resolver miss must humanize, got %v", byPath["qty"].Field)
	}
}
