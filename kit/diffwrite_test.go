package chroniclekit

import "testing"

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
