package chroniclekit

import (
	"context"
	"reflect"
	"strings"
	"testing"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
	"github.com/zdirnecamlcs96/chronicle/kit/internal/memlog"
)

func put(path, to string) changelog.Change {
	return changelog.Change{Actor: "a", Path: path, Kind: KindPut, To: to}
}

func TestReconstruct_UnknownKindErrors(t *testing.T) {
	ctx := context.Background()
	k := NewWithService(memlog.NewService())
	if _, err := k.RecordChanges(ctx, "doc", []changelog.Change{{Actor: "a", Path: "p", Kind: "merge", To: "1"}}); err != nil {
		t.Fatal(err)
	}
	_, err := k.State(ctx, "doc")
	if err == nil || !strings.Contains(err.Error(), `unknown change kind "merge"`) {
		t.Fatalf("err = %v, want unknown-kind error", err)
	}
}

func TestDottedKeyEndToEnd(t *testing.T) {
	// The actual bug being fixed: an object key containing "." must survive
	// Diff → seal → State.
	ctx := context.Background()
	k := NewWithService(memlog.NewService())
	after := map[string]any{"a.b": 1, "c": map[string]any{"d.e": "x"}}
	if _, err := k.RecordUpdate(ctx, "doc", map[string]any{}, after); err != nil {
		t.Fatal(err)
	}
	st, err := k.State(ctx, "doc")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(st, norm(t, after)) {
		t.Fatalf("state = %#v, want %#v", st, norm(t, after))
	}
}
