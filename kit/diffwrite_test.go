package chroniclekit

import (
	"strconv"
	"testing"
)

// decimalVT is a caller-declared value type: {amount string, precision int}
// canonicalized to a bare JSON number, so "10.50"/2 and "10.5"/1 are equal.
var decimalVT = ValueType{
	Fields: []string{"amount", "precision"},
	Canon: func(obj map[string]any) (string, bool) {
		s, ok := obj["amount"].(string)
		if !ok {
			return "", false
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return "", false
		}
		return strconv.FormatFloat(f, 'f', -1, 64), true
	},
}

func TestDiff_ValueTypes(t *testing.T) {
	opt := WithValueTypes(decimalVT)

	// Different encodings of the same value are not a change.
	same, err := Diff(
		map[string]any{"price": map[string]any{"amount": "10.50", "precision": 2}},
		map[string]any{"price": map[string]any{"amount": "10.5", "precision": 1}},
		opt)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(same) != 0 {
		t.Fatalf("equal encodings must not diff, got %+v", same)
	}

	// A real change is one put carrying the canonical forms.
	chg, err := Diff(
		map[string]any{"price": map[string]any{"amount": "10.50", "precision": 2}},
		map[string]any{"price": map[string]any{"amount": "11", "precision": 0}},
		opt)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(chg) != 1 || chg[0].Path != "price" || chg[0].Kind != KindPut ||
		chg[0].From != "10.5" || chg[0].To != "11" {
		t.Fatalf("want single canonical put on price, got %+v", chg)
	}

	// Canon declining (unparsable amount) falls back to normal object diff.
	declined, err := Diff(
		map[string]any{"price": map[string]any{"amount": "abc", "precision": 2}},
		map[string]any{"price": map[string]any{"amount": "def", "precision": 2}},
		opt)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(declined) != 1 || declined[0].Path != "price.amount" {
		t.Fatalf("declined canon must field-diff, got %+v", declined)
	}

	// Key-set mismatch is not a value type.
	mismatch, err := Diff(
		map[string]any{"price": map[string]any{"amount": "1", "precision": 0, "note": "x"}},
		map[string]any{"price": map[string]any{"amount": "2", "precision": 0, "note": "x"}},
		opt)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(mismatch) != 1 || mismatch[0].Path != "price.amount" {
		t.Fatalf("extra key must field-diff, got %+v", mismatch)
	}

	// Create/delete of the shape record the canonical form.
	created, err := Diff(map[string]any{},
		map[string]any{"price": map[string]any{"amount": "10.50", "precision": 2}}, opt)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(created) != 1 || created[0].Kind != KindCreate || created[0].To != "10.5" {
		t.Fatalf("create must record canonical To, got %+v", created)
	}
	deleted, err := Diff(
		map[string]any{"price": map[string]any{"amount": "10.50", "precision": 2}},
		map[string]any{}, opt)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(deleted) != 1 || deleted[0].Kind != KindDelete || deleted[0].From != "10.5" {
		t.Fatalf("delete must record canonical From, got %+v", deleted)
	}
}

func TestDiff_IgnoredFields(t *testing.T) {
	before := map[string]any{
		"status":     "open",
		"updated_at": "2024-01-01T00:00:00Z",
		"meta":       map[string]any{"rev": 1, "note": "x"},
	}
	after := map[string]any{
		"status":     "open",
		"updated_at": "2024-02-02T00:00:00Z",
		"meta":       map[string]any{"rev": 2, "note": "x"},
	}

	// Without the option the bookkeeping edits are ordinary changes.
	plain, err := Diff(before, after)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(plain) != 2 {
		t.Fatalf("without option want 2 changes, got %d: %+v", len(plain), plain)
	}

	// With it they vanish at every depth.
	quiet, err := Diff(before, after, WithIgnoredFields("updated_at", "rev"))
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(quiet) != 0 {
		t.Fatalf("with option want 0 changes, got %d: %+v", len(quiet), quiet)
	}

	// Suppression also mutes create/delete of the field itself.
	created, err := Diff(map[string]any{}, map[string]any{"updated_at": "now", "status": "open"},
		WithIgnoredFields("updated_at"))
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(created) != 1 || created[0].Path != "status" {
		t.Fatalf("want only status create, got %+v", created)
	}
}
