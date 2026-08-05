package chroniclekit

import (
	"reflect"
	"strconv"
	"testing"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
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

// --- keyed arrays -----------------------------------------------------------

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

var linesKey = WithArrayKeys(map[string]string{"lines": "product.id"})

func TestDiff_KeyedArray_Edit(t *testing.T) {
	got, err := Diff(lines(el("P1", 1), el("P2", 5)), lines(el("P1", 3), el("P2", 5)), linesKey)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(got) != 1 || got[0].Path != "lines.0.qty" || got[0].Kind != KindPut ||
		got[0].From != "1" || got[0].To != "3" {
		t.Fatalf("want single put lines.0.qty 1->3, got %+v", got)
	}
}

func TestDiff_KeyedArray_PureReorder(t *testing.T) {
	before := lines(el("P1", 1), el("P2", 5))
	after := lines(el("P2", 5), el("P1", 1))

	keyed, err := Diff(before, after, linesKey)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(keyed) != 0 {
		t.Fatalf("keyed reorder must be silent, got %+v", keyed)
	}

	positional, err := Diff(before, after)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(positional) == 0 {
		t.Fatalf("positional reorder must surface changes (documented)")
	}
}

func TestDiff_KeyedArray_AddRemove(t *testing.T) {
	// P1 (idx 0) and P3 (idx 2) removed, P4 added; deletes must be descending.
	got, err := Diff(
		lines(el("P1", 1), el("P2", 2), el("P3", 3)),
		lines(el("P2", 2), el("P4", 4)),
		linesKey)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 changes, got %+v", got)
	}
	if got[0].Kind != KindDelete || got[0].Path != "lines.2" || got[0].To != "" {
		t.Fatalf("first must delete lines.2, got %+v", got[0])
	}
	if got[1].Kind != KindDelete || got[1].Path != "lines.0" {
		t.Fatalf("second must delete lines.0, got %+v", got[1])
	}
	if got[2].Kind != KindCreate || got[2].Path != "lines.1" || got[2].From != "" {
		t.Fatalf("third must create lines.1 (appended after survivor), got %+v", got[2])
	}
}

func TestDiff_KeyedArray_ResolutionChain(t *testing.T) {
	// Configured key beats the id convention: pairing by "k" vs by "id" yields
	// different changed paths.
	before := map[string]any{"arr": []any{
		map[string]any{"k": "a", "id": "1"},
		map[string]any{"k": "b", "id": "2"},
	}}
	after := map[string]any{"arr": []any{
		map[string]any{"k": "b", "id": "1"},
		map[string]any{"k": "a", "id": "2"},
	}}
	byK, err := Diff(before, after, WithArrayKeys(map[string]string{"arr": "k"}))
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	for _, c := range byK {
		if c.Path != "arr.0.id" && c.Path != "arr.1.id" {
			t.Fatalf("configured key must pair by k (changes on .id), got %+v", byK)
		}
	}
	byID, err := Diff(before, after) // falls to the id convention
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	for _, c := range byID {
		if c.Path != "arr.0.k" && c.Path != "arr.1.k" {
			t.Fatalf("id convention must pair by id (changes on .k), got %+v", byID)
		}
	}

	// Configured key missing a value on an element -> falls to id convention
	// (elements here carry a top-level id, which the convention requires).
	fell, err := Diff(
		map[string]any{"lines": []any{map[string]any{"id": "P1", "qty": 1}, map[string]any{"id": "P2", "qty": 2}}},
		map[string]any{"lines": []any{map[string]any{"id": "P2", "qty": 2}, map[string]any{"id": "P1", "qty": 1}}},
		WithArrayKeys(map[string]string{"lines": "sku"}))
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(fell) != 0 {
		t.Fatalf("missing configured key must fall to id (reorder silent), got %+v", fell)
	}

	// Duplicate ids on one side -> positional.
	dup, err := Diff(
		map[string]any{"arr": []any{map[string]any{"id": "x", "v": 1}, map[string]any{"id": "x", "v": 2}}},
		map[string]any{"arr": []any{map[string]any{"id": "x", "v": 2}, map[string]any{"id": "x", "v": 1}}})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(dup) == 0 {
		t.Fatalf("duplicate ids must fall to positional (reorder surfaces)")
	}
}

// Arrays of scalars (strings/numbers) always compare by index — identity
// applies to arrays of objects only.
func TestDiff_ScalarArraysPositional(t *testing.T) {
	strs, err := Diff(
		map[string]any{"tags": []any{"a", "b"}},
		map[string]any{"tags": []any{"b", "a"}},
		WithArrayKeys(map[string]string{"tags": "id"})) // config cannot force identity onto scalars
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(strs) != 2 || strs[0].Path != "tags.0" || strs[1].Path != "tags.1" {
		t.Fatalf("string array must diff positionally, got %+v", strs)
	}

	nums, err := Diff(
		map[string]any{"n": []any{1, 2, 3}},
		map[string]any{"n": []any{1, 2}})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(nums) != 1 || nums[0].Kind != KindDelete || nums[0].Path != "n.2" {
		t.Fatalf("number array must diff positionally, got %+v", nums)
	}

	// Mixed object/scalar elements: no identity possible -> positional.
	mixed, err := Diff(
		map[string]any{"arr": []any{map[string]any{"id": "x"}, "s"}},
		map[string]any{"arr": []any{"s", map[string]any{"id": "x"}}})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(mixed) == 0 {
		t.Fatalf("mixed-element array must diff positionally")
	}
}

func TestDiff_KeyedArray_RootArray(t *testing.T) {
	got, err := Diff(
		[]any{el("P1", 1), el("P2", 2)},
		[]any{el("P2", 2), el("P1", 1)},
		WithArrayKeys(map[string]string{"": "product.id"}))
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("root-array keyed reorder must be silent, got %+v", got)
	}
}

// TestDiff_KeyedArray_ReplayGate: replaying a keyed diff yields a document
// set-equal per key (survivors in before order + additions appended) and
// DeepEqual outside arrays; with no reorder it is fully DeepEqual.
func TestDiff_KeyedArray_ReplayGate(t *testing.T) {
	before := map[string]any{
		"status": "open",
		"lines":  []any{el("A", 1), el("B", 2), el("C", 3), el("D", 4)},
	}
	after := map[string]any{
		"status": "paid",
		"lines":  []any{el("D", 40), el("B", 2), el("E", 5)},
	}

	build, err := Diff(nil, before, linesKey)
	if err != nil {
		t.Fatalf("build diff: %v", err)
	}
	change, err := Diff(before, after, linesKey)
	if err != nil {
		t.Fatalf("change diff: %v", err)
	}
	got, err := Reconstruct([]changelog.Commit{{Changes: build}, {Changes: change}})
	if err != nil {
		t.Fatalf("reconstruct: %v", err)
	}
	doc := got
	if doc["status"] != "paid" {
		t.Fatalf("non-array part mismatch: %v", doc["status"])
	}
	gotByID := elemsByID(t, doc["lines"])
	wantByID := elemsByID(t, norm(t, after).(map[string]any)["lines"])
	if !reflect.DeepEqual(gotByID, wantByID) {
		t.Fatalf("set mismatch:\n got  %#v\n want %#v", gotByID, wantByID)
	}
	// Documented order semantics: survivors before-order, additions appended.
	gotOrder := idsOf(t, doc["lines"])
	if !reflect.DeepEqual(gotOrder, []string{"B", "D", "E"}) {
		t.Fatalf("replay order must be survivors+additions, got %v", gotOrder)
	}

	// No reorder -> full DeepEqual round trip.
	after2 := map[string]any{
		"status": "open",
		"lines":  []any{el("A", 9), el("B", 2), el("C", 3), el("D", 4), el("F", 6)},
	}
	change2, err := Diff(before, after2, linesKey)
	if err != nil {
		t.Fatalf("change2 diff: %v", err)
	}
	got2, err := Reconstruct([]changelog.Commit{{Changes: build}, {Changes: change2}})
	if err != nil {
		t.Fatalf("reconstruct2: %v", err)
	}
	if !reflect.DeepEqual(got2, norm(t, after2)) {
		t.Fatalf("no-reorder keyed diff must round-trip exactly:\n got  %#v\n want %#v", got2, norm(t, after2))
	}
}

func elemsByID(t *testing.T, v any) map[string]any {
	t.Helper()
	arr, ok := v.([]any)
	if !ok {
		t.Fatalf("not an array: %#v", v)
	}
	out := map[string]any{}
	for _, e := range arr {
		id := e.(map[string]any)["product"].(map[string]any)["id"].(string)
		out[id] = e
	}
	return out
}

func idsOf(t *testing.T, v any) []string {
	t.Helper()
	arr := v.([]any)
	ids := make([]string, len(arr))
	for i, e := range arr {
		ids[i] = e.(map[string]any)["product"].(map[string]any)["id"].(string)
	}
	return ids
}
