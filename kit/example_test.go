package chroniclekit

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/zdirnecamlcs96/chronicle/kit/internal/memlog"

	chroniclediff "github.com/zdirnecamlcs96/chronicle/kit/diff"
	chronicleexplain "github.com/zdirnecamlcs96/chronicle/kit/explain"
	chronicleschema "github.com/zdirnecamlcs96/chronicle/kit/schema"
)

// Example is the batteries-included flow: diff-and-seal writes, then the
// human-friendly reads — a changelog feed and point-in-time states.
// memlog is the kit's internal in-memory test backend, which hands back a
// Service; production code passes an adapter's Log straight to New(yourLog).
func Example() {
	ctx := context.Background()
	k := NewWithService(memlog.NewService())

	// Create: RecordUpdate diffs before→after and seals the result.
	v1 := map[string]any{"status": "open", "total": 100}
	first, err := k.RecordUpdate(ctx, "invoice-42", nil, v1, WithMessage("create invoice"))
	if err != nil {
		panic(err)
	}

	// Update, stamping who did it: diff, set the actor, seal.
	v2 := map[string]any{"status": "paid", "total": 120}
	changes, err := chroniclediff.Diff(v1, v2)
	if err != nil {
		panic(err)
	}
	for i := range changes {
		changes[i].Actor = "alice"
	}
	if _, err := k.RecordChanges(ctx, "invoice-42", changes, WithMessage("mark paid")); err != nil {
		panic(err)
	}

	// The changelog feed: newest first (like git log), one row per change.
	commits, _ := k.Service().Commits(ctx, "invoice-42", 0)
	for _, c := range commits {
		fmt.Printf("%s (authors=%v)\n", c.Message, c.Authors)
		for _, ch := range c.Changes {
			fmt.Printf("  %s %s: %s -> %s\n", ch.Kind, ch.Path, ch.From, ch.To)
		}
	}

	// Point-in-time reads: HEAD and as-of the first commit.
	nowAny, _ := k.State(ctx, "invoice-42")
	thenAny, _ := k.StateAt(ctx, "invoice-42", first.ID)
	now, then := nowAny.(map[string]any), thenAny.(map[string]any)
	fmt.Printf("now:  status=%v total=%v\n", now["status"], now["total"])
	fmt.Printf("then: status=%v total=%v\n", then["status"], then["total"])
	// Output:
	// mark paid (authors=[alice])
	//   put status: "open" -> "paid"
	//   put total: 100 -> 120
	// create invoice (authors=[])
	//   create status:  -> "open"
	//   create total:  -> 100
	// now:  status=paid total=120
	// then: status=open total=100
}

// Example_explain is the read side end to end: declare the document's schema
// once, record with it, then replay the chain into display-ready rows. The
// same chronicleschema.Option slice goes to both sides — identity shapes what is recorded,
// everything else only decorates at read time.
func Example_explain() {
	ctx := context.Background()
	k := NewWithService(memlog.NewService())

	// 1. Declare the schema once. This is the whole vocabulary.
	opts := []chronicleschema.Option{
		// Write-side: how array elements are identified. "lines" is keyed by a
		// dot-path into the embedded entity; other arrays fall back to "_id".
		chronicleschema.WithArrayKeys(map[string]string{"lines": "product._id"}),
		chronicleschema.WithIdentityFields("_id"),
		// Read-side: how things are named, labelled, and folded.
		chronicleschema.WithNameFields("name"),
		chronicleschema.WithIgnoredFields("meta.rev"),
		chronicleschema.WithNames(map[string]string{"t1": "Fragile"}), // entity lives elsewhere
	}

	// 2. Record. RecordUpdate diffs before→after and seals the result.
	v1 := map[string]any{
		"status": "open",
		"meta":   map[string]any{"rev": 1},
		"lines": []any{
			map[string]any{"product": map[string]any{"_id": "P1", "name": "NP1"}, "qty": 1},
		},
	}
	v2 := map[string]any{
		"status": "open",
		"meta":   map[string]any{"rev": 2},
		"lines": []any{
			map[string]any{"product": map[string]any{"_id": "P1", "name": "NP1"}, "qty": 3},
			map[string]any{
				"product": map[string]any{"_id": "P2", "name": "NP2"},
				"qty":     2,
				"tagIds":  []any{"t1", "t9"},
			},
		},
	}
	if _, err := k.RecordUpdate(ctx, "order-7", nil, v1, WithActor("alice"), WithDiffOptions(opts...)); err != nil {
		panic(err)
	}
	if _, err := k.RecordUpdate(ctx, "order-7", v1, v2, WithActor("bob"), WithDiffOptions(opts...)); err != nil {
		panic(err)
	}

	// 3. Read the chain. Commits is newest-first; Explain replays oldest-first.
	commits, err := k.Service().Commits(ctx, "order-7", 0)
	if err != nil {
		panic(err)
	}
	slices.Reverse(commits)
	rows, err := chronicleexplain.Explain(commits, opts...)
	if err != nil {
		panic(err)
	}

	// 4. Render. The kit supplies structure; joining and styling are yours.
	for _, r := range rows[1] { // the update commit
		where := strings.Join(r.Field, " > ")
		if r.Element != nil {
			where = strings.Join(append(r.Element.Trail, r.Element.Name), " > ") + " | " + where
		}
		fmt.Printf("%s: %s -> %s", where, blank(r.From), blank(r.To))
		if r.Display != nil {
			fmt.Printf(" (%s -> %s)", blank(r.Display.From), blank(r.Display.To))
		}
		if r.Bookkeeping {
			fmt.Print(" [bookkeeping]")
		}
		fmt.Println()
		printTree(r.ToValue, "    ")
	}

	// Output:
	// Lines > NP1 | Qty: 1 -> 3
	// Lines > NP2 | : ∅ -> {"product":{"_id":"P2","name":"NP2"},"qty":2,"tagIds":["t1","t9"]}
	//     NP2
	//       Product
	//         Id = "P2" (NP2)
	//         Name = "NP2"
	//       Qty = 2
	//       Tag Ids
	//         0 = "t1" (Fragile)
	//         1 = "t9"
	// Meta > Rev: 1 -> 2 [bookkeeping]
}

// printTree walks a chronicleexplain.ValueNode tree. Value is always the stored canonical
// scalar; chronicleexplain.Display is the resolved name when that scalar is a known id.
func printTree(n *chronicleexplain.ValueNode, indent string) {
	if n == nil {
		return
	}
	switch {
	case n.Kids != nil:
		fmt.Printf("%s%s\n", indent, n.Label)
	case n.Display != "":
		fmt.Printf("%s%s = %s (%s)\n", indent, n.Label, n.Value, n.Display)
	default:
		fmt.Printf("%s%s = %s\n", indent, n.Label, n.Value)
	}
	for i := range n.Kids {
		printTree(&n.Kids[i], indent+"  ")
	}
}

func blank(s string) string {
	if s == "" {
		return "∅"
	}
	return s
}
