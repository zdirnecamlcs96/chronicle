package chroniclekit

import (
	"context"
	"fmt"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
	"github.com/zdirnecamlcs96/chronicle/kit/internal/memlog"
)

// Example_collaborativeEdit is the audit trail for a collaboratively edited
// document — the Google-Docs shape, where several people work on one document
// at once and the last write to a field wins.
//
// chronicle does not implement any of that: settling the order is the CRDT
// layer's job. What it does is keep the resulting audit chain, and the useful
// part is that Changes is an ORDERED LIST OF OPERATIONS rather than a snapshot.
// Replaying it yields the converged document — last write wins, because the
// last write is replayed last — while every actor's operation stays on the
// record, including the ones that lost.
func Example_collaborativeEdit() {
	ctx := context.Background()
	k := NewWithService(memlog.NewService())

	base := map[string]any{"title": "Q3 plan", "budget": 1000}
	if _, err := k.RecordUpdate(ctx, "doc-9", nil, base,
		WithActor("alice"), WithMessage("create plan")); err != nil {
		panic(err)
	}

	// Three people edit at once. The CRDT layer has already settled the order;
	// chronicle records that order verbatim. Note two writes to `budget`.
	ops := []changelog.Change{
		{Path: "budget", Kind: KindPut, From: "1000", To: "1500", Actor: "bob"},
		{Path: "title", Kind: KindPut, From: `"Q3 plan"`, To: `"Q3 plan (final)"`, Actor: "carol"},
		{Path: "budget", Kind: KindPut, From: "1500", To: "1800", Actor: "dave"},
	}
	commit, err := k.RecordChanges(ctx, "doc-9", ops, WithMessage("collaborative edit"))
	if err != nil {
		panic(err)
	}

	state, err := k.State(ctx, "doc-9")
	if err != nil {
		panic(err)
	}
	fmt.Printf("converged: %v\n", state)
	fmt.Printf("authors:   %v\n", commit.Authors)
	for _, ch := range commit.Changes {
		fmt.Printf("  %-6s %-7s %s -> %s\n", ch.Actor, ch.Path, ch.From, ch.To)
	}

	// Output:
	// converged: map[budget:1800 title:Q3 plan (final)]
	// authors:   [bob carol dave]
	//   bob    budget  1000 -> 1500
	//   carol  title   "Q3 plan" -> "Q3 plan (final)"
	//   dave   budget  1500 -> 1800
}
