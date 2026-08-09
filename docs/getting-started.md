---
title: Getting started
permalink: /documentation/getting-started/
eyebrow: getting started
source: getting-started.md
summary: >-
  One program, grown five times, from go get to a durable tamper-evident
  changelog. You hand it a document before and after an edit; it works out what
  changed, who changed it, and seals that into a chain you can verify.
---
{%- assign u_why = '/documentation/why/' | relative_url -%}
{%- assign u_model = '/documentation/model/' | relative_url -%}
{%- assign u_kit = '/documentation/kit/' | relative_url -%}
{%- assign u_reference = '/documentation/reference/' | relative_url -%}
{%- assign u_operations = '/documentation/operations/' | relative_url -%}
{%- assign u_concepts = '/documentation/concepts/' | relative_url -%}
{%- assign u_architecture = '/documentation/architecture/' | relative_url -%}

This page grows one program. Steps 1–4 need nothing but Go; step 5 needs a
MySQL or ClickHouse you can reach.

You never write a `Change` by hand here. You pass the document before and after
an edit, and the library works out the difference — that is the path most
applications want, and it is the shortest one.

## 1. Install

Three modules: the core, a backend, and the kit that diffs documents for you.

```sh
go mod init example.com/myapp
go get github.com/zdirnecamlcs96/chronicle/core@latest
go get github.com/zdirnecamlcs96/chronicle/adapters/memory@latest
go get github.com/zdirnecamlcs96/chronicle/kit@latest
```

One naming quirk, then never again: the import path ends in `core`, the package
is called `changelog`. Alias it and move on.

```go
import changelog "github.com/zdirnecamlcs96/chronicle/core"
```

## 2. Record a change

Two lines of setup — pick a backend, wrap it in a kit — then record.
`RecordUpdate` diffs `before → after` and seals the difference as one commit.

Save as `main.go` and run it:

```go
package main

import (
    "context"
    "fmt"

    changelogmemory "github.com/zdirnecamlcs96/chronicle/adapters/memory"
    chroniclekit "github.com/zdirnecamlcs96/chronicle/kit"
)

func main() {
    ctx := context.Background()

    log := changelogmemory.New()      // the backend; step 5 swaps this line
    k := chroniclekit.New(log)

    invoice := map[string]any{"status": "draft", "total": 1200}
    _, err := k.RecordUpdate(ctx, "invoice-42", nil, invoice,
        chroniclekit.WithActor("alice"),
        chroniclekit.WithMessage("create invoice"))
    if err != nil {
        panic(err)
    }

    sent := map[string]any{"status": "sent", "total": 1200}
    c, err := k.RecordUpdate(ctx, "invoice-42", invoice, sent,
        chroniclekit.WithActor("bob"),
        chroniclekit.WithMessage("send invoice"))
    if err != nil {
        panic(err)
    }
    fmt.Printf("sealed %s\n", c.ID[:7])
}
```

Three things that are worth knowing now, and nothing else yet:

- **`nil` as the before state means "this document did not exist"** — the
  create. Every later call passes the previous state.
- **`WithActor` is who.** Without it a commit seals with no authors, and "who
  changed it" is [half the reason to keep a changelog]({{ u_why }}).
- **Structs work as well as maps.** Both sides are JSON-normalized first,
  honouring `json` tags.

`adapters/memory` keeps everything in a map and loses it when the process
exits. It is for dev and tests, never production; step 5 fixes that.

## 3. Read it back

Two questions a changelog answers. Add to `main`:

```go
state, _ := k.State(ctx, "invoice-42")            // what is it now
fmt.Println("now:", state)

commits, _ := k.Service().Commits(ctx, "invoice-42", 0)   // how did it get here
for _, c := range commits {
    fmt.Printf("%s  %-15s %v\n", c.ID[:7], c.Message, c.Authors)
    for _, ch := range c.Changes {
        fmt.Printf("        %s %s: %s -> %s\n", ch.Kind, ch.Path, ch.From, ch.To)
    }
}

past, _ := k.StateAt(ctx, "invoice-42", commits[len(commits)-1].ID)
fmt.Println("at the root commit:", past)
```

```
now: map[status:sent total:1200]
f089cc5  send invoice    [bob]
        put status: "draft" -> "sent"
b9404a3  create invoice  [alice]
        create status:  -> "draft"
        create total:  -> 1200
at the root commit: map[status:draft total:1200]
```

Current state is never stored — `State` replays the chain to derive it, and
`StateAt` replays it as far as a commit you name. `Commits` is newest-first,
like `git log`; `0` means all.

Your commit ids will differ from the ones above. Each change carries the
timestamp it was staged at, and that timestamp is inside the hash.

## 4. Verify the chain

The reason commits are hashed is so you can detect that stored history was
edited afterwards. One call, against the backend — this is the first step that
needs the core import, so add it:

```go
import changelog "github.com/zdirnecamlcs96/chronicle/core"

if err := changelog.Verify(ctx, log, "invoice-42"); err != nil {
    panic(err) // history was tampered with, or the chain is not linear
}
fmt.Println("verify: ok")
```

`Verify` recomputes every commit's id from its content and checks every parent
link. It reads the whole document each time; once you have a head you already
trust, `VerifyAfter` checks only what came after it. Both, and what each failure
means, are in [reference]({{ u_reference }}).

## 5. Go durable

Everything above works unchanged against a real database. **One line changes.**

```sh
go get github.com/zdirnecamlcs96/chronicle/adapters/sql@latest
```

```go
log, err := changelogsql.Open(ctx,
    "user:pass@tcp(127.0.0.1:3306)/changelog?parseTime=true",
    changelogsql.WithMigrate(true))
if err != nil {
    panic(err)
}
defer log.Close()

k := chroniclekit.New(log) // identical to step 2
```

The DSN must carry `parseTime=true`, or `DATETIME` will not scan into
`time.Time`. `adapters/clickhouse` swaps in the same way with a
`clickhouse://…` DSN.

Use MySQL when two writers can touch one document at once and you need a single
linear chain; use ClickHouse for cheap retention and analytical queries, where
one producer serializes per document. [Operations]({{ u_operations }}) has the
comparison and the tuning that goes with each.

That is the whole loop: record, read, verify, durable.

## What you skipped

You did not need any of this, which is why it is down here.

| | What it is | When you want it |
|---|---|---|
| `Recorder` | Stage changes one at a time, seal when ready — `git add` then `git commit` | You build changes yourself instead of diffing documents |
| `Service` | The facade under the kit: retries, cross-document reads, idempotency | You want commits across all documents, or exactly-once from a queue |
| Capabilities | Four optional interfaces backends may implement | Never, until you write a backend — all three shipped adapters implement all four |
| `Explain` | Replays history into labelled, human-readable rows | You are rendering history in a UI — [the kit page]({{ u_kit }}) |

The one thing genuinely worth deciding early: **if your documents contain
arrays of objects, declare how their elements are identified before your first
write.** Commits sealed without that are recorded positionally, and no later
declaration can re-key them. It is one option, and [the kit
page]({{ u_kit }}) covers it. Add `WithStrictIdentity()` alongside it and the
kit refuses such a write instead of recording it silently — cheap insurance
against the one mistake you cannot repair.

## Before production

- **Pin versions.** v0, experimental; modules version independently
  (`core/vX.Y.Z`, `adapters/sql/vX.Y.Z`).
- **Not `adapters/memory`.** Dev and tests only.
- **`kit/httpapi` ships no auth, authorization, or rate limiting.** If you mount
  it, that is yours to add.
- **Read [operations]({{ u_operations }})** for pool sizing, the ClickHouse
  `FINAL` cost, pruning idempotency keys, and backups.

## Where to go next

| If you want to… | Read |
|---|---|
| render history for humans | [the kit, end to end]({{ u_kit }}) |
| know exactly what a call guarantees | [reference]({{ u_reference }}) |
| understand why it is shaped like git | [the git model]({{ u_model }}) |
| run it for real | [operations]({{ u_operations }}) |
| know the guarantees and the patterns behind them | [concepts]({{ u_concepts }}) |
| write your own backend | [architecture]({{ u_architecture }}) |
