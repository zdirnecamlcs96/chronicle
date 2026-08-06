---
title: The kit, end to end
permalink: /kit/
eyebrow: kit
source: kit.md
summary: >-
  chroniclekit is the batteries-included layer over core — diff a document into
  changes, seal them, replay them back into display-ready rows. One schema
  vocabulary drives both sides. This page walks the whole flow with code that
  the test suite verifies.
---
{%- assign src = site.repo | append: '/blob/main' -%}

`core` deliberately knows nothing about documents, paths, or comparison. It
stores changes and hash-chains them. Everything that turns a document into
changes and back again lives in `kit/` (package `chroniclekit`), which imports
only `core` plus the standard library.

Every snippet below is taken from `Example_explain` in
[`kit/example_test.go`]({{ src }}/kit/example_test.go) — a runnable example whose
output `go test` checks, so nothing on this page can drift from the code.

## The shape of the flow

```
your document  ──Diff──▶  []Change  ──Seal──▶  hash-chained commits
                                                      │
                                                   Commits()
                                                      ▼
display-ready rows  ◀──Explain──  replay the chain
```

Write and read are given the **same** `[]DiffOption`. That is the whole point
of the vocabulary: declare your document's schema once, hand it to both sides.

## 1. Declare the schema

```go
opts := []DiffOption{
    // Write-side: how array elements are identified. "lines" is keyed by a
    // dot-path into the embedded entity; other arrays fall back to "_id".
    WithArrayKeys(map[string]string{"lines": "product._id"}),
    WithIdentityFields("_id"),
    // Read-side: how things are named, labelled, and folded.
    WithNameFields("name"),
    WithIgnoredFields("meta.rev"),
    WithNames(map[string]string{"t1": "Fragile"}), // entity lives elsewhere
}
```

The options split into two groups, and the split matters more than it looks:

| | Write side (`Diff`, `RecordUpdate`) | Read side (`Explain`, `State`) |
|---|---|---|
| Options | `WithArrayKeys`, `WithIdentityFields`, `WithValueTypes` | those two, plus `WithLabels`, `WithNameFields`, `WithNames`, `WithIgnoredFields` |
| Output | `[]Change`, **hash-sealed** into a commit | rows and value trees, **stored nowhere** |
| Getting it wrong | permanent — the changes are the hash preimage, and no later option re-keys history | free — change the option, all existing history re-renders |

**Identity is the one declaration you cannot take back.** It decides whether an
array edit records as `lines.0.qty: 1 → 3` against a stable element, or as a
positional rewrite of every field after an insert. The kit ships **no default**
— it knows no field names, so an array with neither a `WithArrayKeys` entry nor
a usable `WithIdentityFields` field pairs positionally. Declare it before the
first write and keep it stable.

Everything else only decorates at read time. Rename a label, add a name field,
fold a new bookkeeping field — all of history re-renders, and not a stored byte
moves.

## 2. Record

`RecordUpdate` diffs `before → after` and seals the result in one call.

```go
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

k := New(changelog.NewService(yourLog))
k.RecordUpdate(ctx, "order-7", nil, v1, WithDiffOptions(opts...))
k.RecordUpdate(ctx, "order-7", v1, v2, WithDiffOptions(opts...))
```

Note the document shape: each line **embeds** the entity it refers to rather
than carrying its fields inline, and `tagIds` holds ids of entities that live
outside this document entirely. Both are ordinary CRUD shapes, and both are
handled below without storing anything extra.

The stored record stays machine-shaped — dotted paths, canonical-JSON scalars:

```
put    lines.0.qty   1 → 3
create lines.1       ∅ → {"product":{"_id":"P2",…},"qty":2,"tagIds":["t1","t9"]}
put    meta.rev      1 → 2
```

## 3. Read the chain

`Commits` is newest-first, like `git log`. `Explain` replays from the root, so
reverse before handing it over.

```go
commits, err := k.Service().Commits(ctx, "order-7", 0)
slices.Reverse(commits)
rows, err := Explain(commits, opts...)
// rows[i][j] decorates commits[i].Changes[j], 1:1
```

`Explain` replays the chain and derives display metadata from the revisions
surrounding each commit. Because it is all derived, records written long before
any schema was declared decorate exactly like new ones.

## 4. Render

Each `Explained` embeds the stored `Change` untouched and adds:

| Field | What it is |
|---|---|
| `Field []string` | label trail of the changed field, element-relative when `Element` is set |
| `Element` | the keyed array element the change sits in (`Trail`, `Name`, `ID`); `Element` with an empty `Field` is a whole-element add/remove |
| `Display` | id-valued `From`/`To` resolved to names |
| `Bookkeeping` | the change touches a `WithIgnoredFields` entry — still recorded, foldable on display |
| `FromValue`/`ToValue` | a container value decomposed as a `ValueNode` tree |

```go
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
```

which prints:

```
Lines > NP1 | Qty: 1 -> 3
Lines > NP2 | : ∅ -> {"product":{"_id":"P2","name":"NP2"},"qty":2,"tagIds":["t1","t9"]}
    NP2
      Product
        Id = "P2" (NP2)
        Name = "NP2"
      Qty = 2
      Tag Ids
        0 = "t1" (Fragile)
        1 = "t9"
Meta > Rev: 1 -> 2 [bookkeeping]
```

Four things happened there, none of which required storing anything:

- **`Lines > NP1`** — the element is keyed by `product._id`, a dot-path. The
  element root has no name of its own, so the display name comes from the
  object the path descends into: `product.name`. The same rule names the value
  tree's root (`NP2`).
- **`Tag Ids › 0 = "t1" (Fragile)`** — `t1` names an entity that is not in this
  document at all. `WithNames` supplied it. `Value` is still the stored
  canonical scalar; `Display` is additive, so a UI can show the name and keep
  the id for a tooltip.
- **`Id = "P2" (NP2)`** — ids found *inside* the document resolve too, with no
  dictionary entry needed. Pairs discovered in the replayed revisions always
  beat a caller-supplied name, so a stale dictionary can never override the
  record.
- **`[bookkeeping]`** — `meta.rev` was recorded in full and flagged, not
  dropped. Folding it is the display's choice.

## What the kit will not do

The boundary is deliberate: **the kit emits structure, never formatting.**
Slices, not joined strings. Names, not sentences. Canonical scalars, not
prettified text. Flags, not decisions.

Separators, truncation, pluralization, verbs, colors, and folding belong to the
consumer — which is why a CLI, a web app, and an email digest can render the
same rows differently, and why the library never limits the styling.

## Over a wire

The option vocabulary is data, not callbacks — `WithNames` in particular is a
map precisely so a schema declared in one process survives serialisation to
another. A `POST /explain {doc, options}` endpoint that runs `Explain`
server-side keeps browser clients free of schema logic, and the server stays
schema-blind too. See [`kit/httpapi`]({{ src }}/kit/httpapi) for the transport
shape.

The one exception is `WithLabels`, which takes a resolver function — it is
where i18n plugs in, and it never crosses a process boundary.
