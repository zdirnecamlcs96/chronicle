package chroniclekit

import (
	"testing"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
	"github.com/zdirnecamlcs96/chronicle/kit/internal/memlog"
	chronicleschema "github.com/zdirnecamlcs96/chronicle/kit/schema"
)

func lines(elems ...map[string]any) map[string]any {
	arr := make([]any, len(elems))
	for i, e := range elems {
		arr[i] = e
	}
	return map[string]any{"lines": arr}
}

func el(id string, qty int) map[string]any {
	return map[string]any{"product": map[string]any{"id": id, "name": "N" + id}, "qty": qty}
}

var linesKey = chronicleschema.WithArrayKeys(map[string]string{"lines": "product.id"})

func TestKit_RecordUpdate_WithDiffOptions(t *testing.T) {
	ctx := t.Context()
	k := NewWithService(memlog.NewService())

	// Keyed reorder diffs to nothing -> core's empty-changes sentinel.
	before := lines(el("P1", 1), el("P2", 2))
	reordered := lines(el("P2", 2), el("P1", 1))
	if _, err := k.RecordUpdate(ctx, "doc", before, reordered, WithDiffOptions(linesKey)); err != changelog.ErrEmptyChanges {
		t.Fatalf("keyed reorder must seal nothing, got err=%v", err)
	}

	// A real edit records the keyed diff; other RecordOptions still apply.
	edited := lines(el("P2", 9), el("P1", 1))
	c, err := k.RecordUpdate(ctx, "doc", before, edited, WithDiffOptions(linesKey), WithMessage("bump"))
	if err != nil {
		t.Fatalf("RecordUpdate: %v", err)
	}
	if len(c.Changes) != 1 || c.Changes[0].Path != "lines.1.qty" || c.Changes[0].To != "9" {
		t.Fatalf("want single keyed change lines.1.qty->9, got %+v", c.Changes)
	}
	if c.Message != "bump" {
		t.Fatalf("message lost: %+v", c)
	}
}
