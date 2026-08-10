package chronicleexplain

import (
	"fmt"
	"reflect"
	"strconv"
	"testing"

	changelog "github.com/zdirnecamlcs96/chronicle/core"

	chroniclediff "github.com/zdirnecamlcs96/chronicle/kit/diff"
	chronicleschema "github.com/zdirnecamlcs96/chronicle/kit/schema"
)

// commitsFor diffs each successive state pair into one commit, oldest first,
// starting from nil — the shape Explain expects (chain from root).
func commitsFor(t *testing.T, states []any, opts ...chronicleschema.Option) []changelog.Commit {
	t.Helper()
	var commits []changelog.Commit
	prev := any(nil)
	for _, s := range states {
		changes, err := chroniclediff.Diff(prev, s, opts...)
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

// TestExplain_BookkeepingFlag: ignored fields are recorded like any other
// data; Explain flags their changes so displays can fold them. An entry is a
// bare name (matched at any depth) or an index-free schema path (matching its
// field and subtree), so a name that is bookkeeping in one branch stays data
// in another.
func TestExplain_BookkeepingFlag(t *testing.T) {
	commits := commitsFor(t, []any{
		map[string]any{
			"status": "open",
			"meta":   map[string]any{"rev": 1, "audit": map[string]any{"by": "u1"}},
			"items":  []any{map[string]any{"id": "I1", "rev": "a", "updated_at": "t1"}},
		},
		map[string]any{
			"status": "closed",
			"meta":   map[string]any{"rev": 2, "audit": map[string]any{"by": "u2"}},
			"items":  []any{map[string]any{"id": "I1", "rev": "b", "updated_at": "t2"}},
		},
	})
	got, err := Explain(commits, chronicleschema.WithIgnoredFields("updated_at", "meta.rev", "meta.audit"))
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	flags := map[string]bool{}
	for _, rows := range got {
		for _, r := range rows {
			flags[r.Path] = r.Bookkeeping
		}
	}
	want := map[string]bool{
		"status":             false, // ordinary data
		"items.0.updated_at": true,  // bare name matches at any depth
		"meta.rev":           true,  // schema path matches its exact field
		"meta.audit.by":      true,  // schema path covers its subtree
		"items.0.rev":        false, // rev outside meta is data, not bookkeeping
	}
	for path, wantFlag := range want {
		if flags[path] != wantFlag {
			t.Fatalf("%s: want Bookkeeping=%v, got %v (all: %v)", path, wantFlag, flags[path], flags)
		}
	}
}

// TestExplain_ValueTrees: container From/To values decompose into ValueNode
// trees under the same schema — labels, element identity, canonical scalars —
// so displays can break a whole-container change down without re-implementing
// the walk.
func TestExplain_ValueTrees(t *testing.T) {
	opts := []chronicleschema.Option{
		chronicleschema.WithArrayKeys(map[string]string{"items": "sku", "items.quantities": "uom"}),
		chronicleschema.WithNameFields("label", "name"),
	}
	doc := map[string]any{
		"items": []any{
			map[string]any{
				"sku": "ING-1", "name": "Flour",
				"quantities": []any{map[string]any{"uom": "kg", "label": "Kilogram", "qty": 12}},
			},
			map[string]any{"sku": "ING-2", "name": "Sugar", "quantities": []any{}},
		},
	}
	commits := commitsFor(t, []any{doc}, opts...)
	got, err := Explain(commits, opts...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	var items *Explained
	for i, r := range got[0] {
		if r.Path == "items" {
			items = &got[0][i]
		}
	}
	if items == nil || items.ToValue == nil || items.FromValue != nil {
		t.Fatalf("items create needs a ToValue tree only: %+v", items)
	}
	root := items.ToValue
	if !root.List || len(root.Kids) != 2 || root.Kids[0].Label != "Flour" || root.Kids[1].Label != "Sugar" {
		t.Fatalf("array kids must carry element names: %+v", root)
	}
	flour := root.Kids[0]
	var qties *ValueNode
	for i, k := range flour.Kids {
		if k.Label == "Quantities" {
			qties = &flour.Kids[i]
		}
	}
	if qties == nil || !qties.List || len(qties.Kids) != 1 || qties.Kids[0].Label != "Kilogram" {
		t.Fatalf("nested keyed array must resolve identity from items.quantities: %+v", flour)
	}
	var qty *ValueNode
	for i, k := range qties.Kids[0].Kids {
		if k.Label == "Qty" {
			qty = &qties.Kids[0].Kids[i]
		}
	}
	if qty == nil || qty.Value != "12" || qty.Kids != nil {
		t.Fatalf("leaf must carry the canonical scalar: %+v", qties.Kids[0])
	}
}

// TestExplain_ValueTreeBookkeeping: container breakdowns carry the same
// bookkeeping mark as rows — a kid for an ignored field (bare name or schema
// path) is flagged so displays can fold noise inside whole-container changes.
func TestExplain_ValueTreeBookkeeping(t *testing.T) {
	commits := commitsFor(t, []any{
		map[string]any{"item": map[string]any{"name": "Flour", "updated_at": "t1", "rev": 1}},
	})
	got, err := Explain(commits, chronicleschema.WithIgnoredFields("updated_at", "item.rev"))
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	row := got[0][0]
	if row.Path != "item" || row.ToValue == nil {
		t.Fatalf("want item create with a ToValue tree: %+v", row)
	}
	flags := map[string]bool{}
	for _, k := range row.ToValue.Kids {
		flags[k.Label] = k.Bookkeeping
	}
	want := map[string]bool{"Name": false, "Updated At": true, "Rev": true}
	for label, wantFlag := range want {
		if flags[label] != wantFlag {
			t.Fatalf("%s: want Bookkeeping=%v, got %v (all: %v)", label, wantFlag, flags[label], flags)
		}
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
	got, err := Explain(commits, chronicleschema.WithLabels(resolver))
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

func TestExplain_KeyedElement(t *testing.T) {
	commits := commitsFor(t, []any{
		lines(el("P1", 1), el("P2", 2)),
		lines(el("P1", 3), el("P2", 2)),
	}, linesKey)
	got, err := Explain(commits, linesKey)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	edit := got[1][0]
	if edit.Path != "lines.0.qty" {
		t.Fatalf("unexpected change: %+v", edit.Change)
	}
	if edit.Element == nil || !reflect.DeepEqual(edit.Element.Trail, []string{"Lines"}) ||
		edit.Element.ID != "P1" || edit.Element.Name != "NP1" {
		t.Fatalf("want Element{[Lines] NP1 P1}, got %+v", edit.Element)
	}
	if !reflect.DeepEqual(edit.Field, []string{"Qty"}) {
		t.Fatalf("Field must be relative to element root, got %v", edit.Field)
	}
}

func TestExplain_WholeElementAddRemove(t *testing.T) {
	commits := commitsFor(t, []any{
		lines(el("P1", 1), el("P2", 2)),
		lines(el("P2", 2), el("P3", 3)), // P1 removed, P3 added
	}, linesKey)
	got, err := Explain(commits, linesKey)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	var del, add *Explained
	for i := range got[1] {
		switch got[1][i].Kind {
		case chronicleschema.KindDelete:
			del = &got[1][i]
		case chronicleschema.KindCreate:
			add = &got[1][i]
		}
	}
	if del == nil || del.Element == nil || del.Element.ID != "P1" || len(del.Field) != 0 {
		t.Fatalf("removal wants Element P1 + empty Field, got %+v", del)
	}
	if add == nil || add.Element == nil || add.Element.ID != "P3" || add.Element.Name != "NP3" || len(add.Field) != 0 {
		t.Fatalf("addition wants Element P3 + empty Field, got %+v", add)
	}
}

// Records written by the pre-feature positional differ decorate identically —
// the metadata comes from the replayed revision, not from the record.
func TestExplain_PreFeatureRecords(t *testing.T) {
	doc := lines(el("P1", 1), el("P2", 2))
	build, err := chroniclediff.Diff(nil, doc) // legacy: no options, positional
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	legacyEdit := changelog.Change{Path: "lines.0.qty", Kind: chronicleschema.KindPut, From: "1", To: "3"}
	commits := []changelog.Commit{{Changes: build}, {Changes: []changelog.Change{legacyEdit}}}

	got, err := Explain(commits, linesKey)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	edit := got[1][0]
	if edit.Element == nil || edit.Element.ID != "P1" || edit.Element.Name != "NP1" ||
		!reflect.DeepEqual(edit.Field, []string{"Qty"}) {
		t.Fatalf("old record must decorate fully, got %+v", edit)
	}
}

func TestExplain_DisplayNames(t *testing.T) {
	commits := commitsFor(t, []any{
		map[string]any{
			"assignee": "u1",
			"users":    []any{map[string]any{"id": "u1", "name": "Alice"}},
		},
		map[string]any{
			"assignee": "u2",
			"users":    []any{map[string]any{"id": "u1", "name": "Alice"}, map[string]any{"id": "u2", "name": "Bob"}},
		},
	}, idKey)
	got, err := Explain(commits, idKey)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	var edit *Explained
	for i := range got[1] {
		if got[1][i].Path == "assignee" {
			edit = &got[1][i]
		}
	}
	if edit == nil {
		t.Fatalf("assignee change missing: %+v", got[1])
	}
	// Bob only exists in the after revision — either revision must resolve.
	if edit.Display == nil || edit.Display.From != "Alice" || edit.Display.To != "Bob" {
		t.Fatalf("want Display Alice->Bob, got %+v", edit.Display)
	}
	if edit.From != `"u1"` || edit.To != `"u2"` {
		t.Fatalf("stored From/To must stay raw ids, got %+v", edit.Change)
	}

	// No known ids -> Display nil.
	plain := commitsFor(t, []any{
		map[string]any{"status": "open"},
		map[string]any{"status": "paid"},
	})
	pg, err := Explain(plain)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if pg[1][0].Display != nil {
		t.Fatalf("unknown values must not get Display, got %+v", pg[1][0].Display)
	}
}

func TestExplain_NameFieldsOverride(t *testing.T) {
	commits := commitsFor(t, []any{
		map[string]any{"docs": []any{map[string]any{"id": "d1", "title": "Spec", "name": ""}}},
		map[string]any{"docs": []any{map[string]any{"id": "d1", "title": "Spec v2", "name": ""}}},
	}, idKey)
	got, err := Explain(commits, chronicleschema.WithNameFields("title"), idKey)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	edit := got[1][0]
	if edit.Element == nil || edit.Element.Name != "Spec" {
		t.Fatalf("WithNameFields must pick title, got %+v", edit.Element)
	}
}

// TestExplain_ElementNameFromIdentityObject: when identity is a dot-path, the
// display name comes from the object that path descends into — the one the id
// belongs to — not from the element root, which carries no name of its own.
func TestExplain_ElementNameFromIdentityObject(t *testing.T) {
	key := chronicleschema.WithArrayKeys(map[string]string{"lines": "product._id"})
	commits := commitsFor(t, []any{
		map[string]any{"lines": []any{
			map[string]any{"product": map[string]any{"_id": "P1", "name": "NP1"}, "qty": 1},
		}},
		map[string]any{"lines": []any{
			map[string]any{"product": map[string]any{"_id": "P1", "name": "NP1"}, "qty": 3},
		}},
	}, key)
	got, err := Explain(commits, key)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	edit := got[1][0]
	if edit.Element == nil || edit.Element.Name != "NP1" || edit.Element.ID != "P1" {
		t.Fatalf("dot-path identity must name from its own object, got %+v", edit.Element)
	}
}

// TestExplain_SingleSegmentKeyUnchanged: a single-segment key has no identity
// object to descend into, so it resolves at the element root exactly as before
// — the identity-object lookup is a strict no-op there.
func TestExplain_SingleSegmentKeyUnchanged(t *testing.T) {
	key := chronicleschema.WithArrayKeys(map[string]string{"lines": "id"})
	commits := commitsFor(t, []any{
		map[string]any{"lines": []any{map[string]any{"id": "L1", "name": "NL1", "qty": 1}}},
		map[string]any{"lines": []any{map[string]any{"id": "L1", "name": "NL1", "qty": 3}}},
	}, key)
	got, err := Explain(commits, key)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	edit := got[1][0]
	if edit.Element == nil || edit.Element.Name != "NL1" || edit.Element.ID != "L1" {
		t.Fatalf("single-segment key must resolve at the element root, got %+v", edit.Element)
	}
}

// TestExplain_WithNames: the caller's dictionary resolves ids whose entities
// live outside the document, and loses to a name the document itself supplies
// — a stale dictionary can never override the record.
func TestExplain_WithNames(t *testing.T) {
	opts := []chronicleschema.Option{idKey, chronicleschema.WithNames(map[string]string{
		"t1": "Fragile", // entity lives outside the document
		"u1": "Stale",   // the document names u1 too, and must win
	})}
	commits := commitsFor(t, []any{
		map[string]any{"tag": "t1", "owner": "u1",
			"users": []any{map[string]any{"id": "u1", "name": "Alice"}}},
		map[string]any{"tag": "t2", "owner": "u2",
			"users": []any{map[string]any{"id": "u1", "name": "Alice"}}},
	}, opts...)
	got, err := Explain(commits, opts...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	byPath := map[string]Explained{}
	for _, r := range got[1] {
		byPath[r.Path] = r
	}
	if d := byPath["tag"].Display; d == nil || d.From != "Fragile" {
		t.Fatalf("dictionary must resolve an id absent from the document, got %+v", d)
	}
	if d := byPath["owner"].Display; d == nil || d.From != "Alice" {
		t.Fatalf("document-derived name must beat the dictionary, got %+v", d)
	}
	if byPath["tag"].From != `"t1"` || byPath["owner"].From != `"u1"` {
		t.Fatalf("stored From/To must stay raw ids, got %+v", got[1])
	}
}

// TestExplain_ValueTreeDisplay: a container value made of ids keeps the record
// in Value and gains the resolved name in Display; unknown ids stay "".
func TestExplain_ValueTreeDisplay(t *testing.T) {
	opts := []chronicleschema.Option{
		chronicleschema.WithArrayKeys(map[string]string{"lines": "id"}),
		chronicleschema.WithNames(map[string]string{"t1": "Fragile"}),
	}
	commits := commitsFor(t, []any{
		map[string]any{"lines": []any{map[string]any{"id": "L1", "qty": 1}}},
		map[string]any{"lines": []any{
			map[string]any{"id": "L1", "qty": 1},
			map[string]any{"id": "L2", "qty": 2, "tagIds": []any{"t1", "t9"}},
		}},
	}, opts...)
	got, err := Explain(commits, opts...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	add := got[1][0]
	if add.ToValue == nil {
		t.Fatalf("whole-element addition must carry a value tree, got %+v", add)
	}
	var tags *ValueNode
	for i, k := range add.ToValue.Kids {
		if k.Label == "Tag Ids" {
			tags = &add.ToValue.Kids[i]
		}
	}
	if tags == nil || len(tags.Kids) != 2 {
		t.Fatalf("want a Tag Ids list of 2, got %+v", add.ToValue.Kids)
	}
	if tags.Kids[0].Value != `"t1"` || tags.Kids[0].Display != "Fragile" {
		t.Fatalf("known id must keep Value and gain Display, got %+v", tags.Kids[0])
	}
	if tags.Kids[1].Value != `"t9"` || tags.Kids[1].Display != "" {
		t.Fatalf("unknown id must keep Value and no Display, got %+v", tags.Kids[1])
	}
}

func TestExplain_RootArrayAndNestedKeyed(t *testing.T) {
	// Root-level array via the "" config key, with a nested keyed array inside:
	// the innermost element wins.
	opts := chronicleschema.WithArrayKeys(map[string]string{"": "id", "subs": "id"})
	before := []any{map[string]any{
		"id":   "R1",
		"name": "Root",
		"subs": []any{map[string]any{"id": "S1", "name": "Sub", "v": 1}},
	}}
	after := []any{map[string]any{
		"id":   "R1",
		"name": "Root",
		"subs": []any{map[string]any{"id": "S1", "name": "Sub", "v": 2}},
	}}
	build, err := chroniclediff.Diff(nil, before, opts)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	change, err := chroniclediff.Diff(before, after, opts)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	got, err := Explain([]changelog.Commit{{Changes: build}, {Changes: change}}, opts)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	edit := got[1][0]
	if edit.Element == nil || edit.Element.ID != "S1" || edit.Element.Name != "Sub" {
		t.Fatalf("innermost element must win, got %+v", edit.Element)
	}
	if !reflect.DeepEqual(edit.Field, []string{"V"}) {
		t.Fatalf("Field relative to innermost element, got %v", edit.Field)
	}
	if !reflect.DeepEqual(edit.Element.Trail, []string{"Subs"}) {
		t.Fatalf("inner trail from document root, got %v", edit.Element.Trail)
	}
}

// idKey declares the generic "id" identity the kit no longer assumes.
var idKey = chronicleschema.WithIdentityFields("id")

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

// moneyVT is the {amount, currency} value type the diff tests exercise; Canon
// declines when amount is not a numeric string.
func moneyVT() chronicleschema.ValueType {
	return chronicleschema.ValueType{
		Fields: []string{"amount", "currency"},
		Canon: func(obj map[string]any) (string, bool) {
			amt, ok := obj["amount"].(string)
			if !ok {
				return "", false
			}
			cur, ok := obj["currency"].(string)
			if !ok {
				return "", false
			}
			f, err := strconv.ParseFloat(amt, 64)
			if err != nil {
				return "", false
			}
			return fmt.Sprintf("%.2f %s", f, cur), true
		},
	}
}

// TestExplain_ValueTypes_NestedLeaf: a value-typed object nested inside a
// container value is a leaf carrying its canonical scalar — the schema says the
// shape is a scalar, so the tree must not decompose it field-by-field.
func TestExplain_ValueTypes_NestedLeaf(t *testing.T) {
	opts := []chronicleschema.Option{chronicleschema.WithValueTypes(moneyVT())}
	commits := commitsFor(t, []any{
		map[string]any{"line": map[string]any{
			"qty":   2,
			"price": map[string]any{"amount": "10.50", "currency": "USD"},
		}},
	}, opts...)
	got, err := Explain(commits, opts...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	row := got[0][0]
	if row.Path != "line" || row.ToValue == nil {
		t.Fatalf("line create needs a ToValue tree: %+v", row)
	}
	var price, qty *ValueNode
	for i, k := range row.ToValue.Kids {
		switch k.Label {
		case "Price":
			price = &row.ToValue.Kids[i]
		case "Qty":
			qty = &row.ToValue.Kids[i]
		}
	}
	if price == nil || price.Value != "10.50 USD" || price.Kids != nil {
		t.Fatalf("value-typed node must be a canonical-scalar leaf: %+v", row.ToValue)
	}
	if qty == nil || qty.Value != "2" {
		t.Fatalf("sibling leaf unchanged: %+v", row.ToValue)
	}
}

// TestExplain_ValueTypes_RootScalar: a value-typed shape stored raw as the
// change's whole value (recorded before the type was declared) keeps scalar
// semantics at read time — no From/To tree, same contract as scalar values.
func TestExplain_ValueTypes_RootScalar(t *testing.T) {
	commits := commitsFor(t, []any{ // no schema at write: the raw object is stored
		map[string]any{"price": map[string]any{"amount": "10.50", "currency": "USD"}},
	})
	got, err := Explain(commits, chronicleschema.WithValueTypes(moneyVT()))
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	row := got[0][0]
	if row.Path != "price" || row.ToValue != nil || row.FromValue != nil {
		t.Fatalf("value-typed root must have nil value trees: %+v", row)
	}
}

// TestExplain_ValueTypes_Declined: an object with an extra key never consults
// Canon (key sets must match exactly), and a matching key set whose Canon
// declines falls back to normal per-field recursion.
func TestExplain_ValueTypes_Declined(t *testing.T) {
	opts := []chronicleschema.Option{chronicleschema.WithValueTypes(moneyVT())}
	commits := commitsFor(t, []any{
		map[string]any{"line": map[string]any{
			"tagged": map[string]any{"amount": "10.50", "currency": "USD", "note": "x"},
			"badamt": map[string]any{"amount": 12, "currency": "USD"},
		}},
	}, opts...)
	got, err := Explain(commits, opts...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	row := got[0][0]
	if row.ToValue == nil {
		t.Fatalf("line create needs a ToValue tree: %+v", row)
	}
	for _, k := range row.ToValue.Kids {
		if len(k.Kids) == 0 {
			t.Fatalf("%s must decompose per-field, got %+v", k.Label, k)
		}
	}
}

// TestExplain_ValueTypes_NoSchema: with no value types declared the same
// commits decompose exactly as before — declaring nothing changes nothing.
func TestExplain_ValueTypes_NoSchema(t *testing.T) {
	opts := []chronicleschema.Option{chronicleschema.WithValueTypes(moneyVT())}
	commits := commitsFor(t, []any{
		map[string]any{"line": map[string]any{
			"price": map[string]any{"amount": "10.50", "currency": "USD"},
		}},
	}, opts...)
	got, err := Explain(commits) // no value types at read
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	var price *ValueNode
	for i, k := range got[0][0].ToValue.Kids {
		if k.Label == "Price" {
			price = &got[0][0].ToValue.Kids[i]
		}
	}
	if price == nil || len(price.Kids) != 2 {
		t.Fatalf("undeclared shape must decompose per-field: %+v", got[0][0].ToValue)
	}
}
