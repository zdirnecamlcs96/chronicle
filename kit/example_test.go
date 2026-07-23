package chroniclekit

import (
	"context"
	"fmt"
)

// Example is the batteries-included flow: diff-and-seal writes, then the
// human-friendly reads — a changelog feed and point-in-time states.
// newMemService is this package's in-memory test backend; production code
// passes any adapter instead: New(changelog.NewService(yourLog)).
func Example() {
	ctx := context.Background()
	k := New(newMemService())

	// Create: RecordUpdate diffs before→after and seals the result.
	v1 := map[string]any{"status": "open", "total": 100}
	first, err := k.RecordUpdate(ctx, "invoice-42", nil, v1, WithMessage("create invoice"))
	if err != nil {
		panic(err)
	}

	// Update, stamping who did it: diff, set the actor, seal.
	v2 := map[string]any{"status": "paid", "total": 120}
	changes, err := Diff(v1, v2)
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
	now, _ := k.State(ctx, "invoice-42")
	then, _ := k.StateAt(ctx, "invoice-42", first.ID)
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
