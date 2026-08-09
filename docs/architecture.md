---
title: Architecture
permalink: /documentation/architecture/
eyebrow: architecture
source: architecture.md
summary: >-
  How chronicle is laid out, for readers who already know Clean Architecture or
  ports-and-adapters. The rings are Go modules rather than folders, the domain
  object is a log rather than a business entity, and the vocabulary is git's.
  This page maps all three onto names you already have.
---
{%- assign src = site.repo | append: '/blob/main' -%}
{%- assign u_design = '/documentation/design/' | relative_url -%}
{%- assign u_kit = '/documentation/kit/' | relative_url -%}
{%- assign u_model = '/documentation/model/' | relative_url -%}
{%- assign u_concepts = '/documentation/concepts/' | relative_url -%}

chronicle is ports-and-adapters wrapped around an event-sourced, git-shaped
write model. If you have read a Clean Architecture codebase before, three things
here will look wrong at first. None of them is an accident, and each has a
one-line explanation.

## The shape, end to end

Something produces `Change`s — CRUD handlers, a message consumer, a state diff.
A `Recorder` or the `Service` facade seals them, and the commit lands in
whatever `Log` you picked. The conformance suite validates every backend against
the same contract.

<div class="diagram" role="img" aria-label="Layers: your app produces changes; the stdlib-only core seals them through the Recorder or Service facade into the 3-method Log port; commits land in the memory, MySQL, or ClickHouse adapter, all validated by the conformance suite.">
{% raw %}<pre class="mermaid">
flowchart TB
    subgraph L1["producers — anything that emits changes"]
        APP["your app&lt;br/&gt;CRUD handlers · message consumer · state diff"]
    end

    subgraph L2["core — stdlib only · package changelog"]
        REC["Recorder&lt;br/&gt;Append(Change) → Commit()"]
        SVC["Service facade&lt;br/&gt;Seal · Commits · AllCommits · Get"]
        PORT{{"Log port&lt;br/&gt;AppendCommit · Commits · Head"}}
        CAP["optional capabilities&lt;br/&gt;Indexer · Deduper · TailReader · Snapshotter"]
    end

    subgraph L3["adapters — optional · pick one, or write your own"]
        MEM["adapters/memory&lt;br/&gt;in-memory · dev/ref"]
        SQL["adapters/sql&lt;br/&gt;MySQL · transactional"]
        CH["adapters/clickhouse&lt;br/&gt;ClickHouse · columnar · eventual"]
    end

    CONF["core/conformance&lt;br/&gt;RunLogConformance · …"]

    APP -->|Recorder| REC
    APP -->|facade| SVC
    REC --> PORT
    SVC --> PORT
    PORT --> MEM
    PORT --> SQL
    PORT --> CH
    MEM -. validated by .-> CONF
    SQL -. validated by .-> CONF
    CH -. validated by .-> CONF

    classDef port fill:#141414,stroke:#D9A441,stroke-width:1.5px,color:#FAFAFA
    classDef conf fill:transparent,stroke:#5FA97C,stroke-dasharray:5 4,color:#5FA97C
    class PORT port
    class CONF conf
</pre>{% endraw %}
</div>

The core stays wire-free. When a Go server wants batteries, the optional `kit`
module layers them on top: state diffing, JSON-Patch conversion, snapshot-backed
`State`/`StateAt`, read-time `Explain` display decoration, and `kit/httpapi` — a
ready-made `http.ServeMux` (`POST /commits`, `GET /commits`,
`GET /commits/{id}`, `GET /changes`, `POST /explain`) you mount behind your own
auth and middleware.

This page is the **map**: what lives where, and how the names translate. Why any
of it is shaped this way — and what each choice cost — is on
[design decisions]({{ u_design }}).

## 1. The rings are modules, not folders

There is no `entities/`, no `usecases/`, no `infrastructure/`. Look for the
layer boundary in `go.mod` files instead — the repo is a Go workspace of five
modules ([`go.work`]({{ src }}/go.work)):

| Ring | Module | Package | Depends on |
|---|---|---|---|
| Domain | `core/` | `changelog` | **nothing** |
| Application | `kit/` + 4 subpackages | `chroniclekit`, … | `core` |
| Infrastructure | `adapters/memory` | `changelogmemory` | `core` |
| Infrastructure | `adapters/sql` | `changelogsql` | `core` + MySQL driver |
| Infrastructure | `adapters/clickhouse` | `changelogclickhouse` | `core` + ClickHouse driver |

**[`core/go.mod`]({{ src }}/core/go.mod) has no `require` block at all. That
absence is the entire dependency rule** — the compiler enforces it, and the same
split quarantines drivers, so using the memory backend never pulls the
ClickHouse client into your `go.sum`. [Why modules rather than folder
discipline, and what it costs at release time]({{ u_design }}#the-core-depends-on-nothing).

Two naming traps while you read. **Directory name ≠ package name**: `core/` is
package `changelog`, `kit/` is `chroniclekit`, `adapters/sql` is
`changelogsql`. And `core/conformance` is a *package inside* the core module,
not a module of its own.

## 2. Clean Architecture, translated

| Clean Architecture | chronicle | Where |
|---|---|---|
| Entities | `Change`, `Commit` | [`core/change.go`]({{ src }}/core/change.go), [`core/commit.go`]({{ src }}/core/commit.go) |
| Use cases / interactors | `Recorder` (one doc), `Service` (cross-doc) | [`core/recorder.go`]({{ src }}/core/recorder.go), [`core/service.go`]({{ src }}/core/service.go) |
| Output port (repository interface) | `Log` — **three methods** | [`core/log.go`]({{ src }}/core/log.go) |
| Repository implementations | `adapters/*` | one module per driver |
| Presenters / view models | `Explain`, `State`, `Diff` | `kit/explain`, `kit/view`, `kit/diff` |
| DTOs and mappers | **none** — `Change` crosses every ring unchanged | — |
| Input port / controllers | **out of scope by design**; `kit/httpapi` is one optional example | [`kit/httpapi`]({{ src }}/kit/httpapi) |
| DI container | a four-line chain, no framework | below |

Wiring the whole stack is one expression:

```go
log, err := changelogsql.Open(ctx, dsn)     // infrastructure
if err != nil {
    panic(err)
}
k := chroniclekit.New(log)                  // application (builds the Service)
h := httpapi.Handler(changelog.NewService(log)) // transport (optional)
```

`chroniclekit.New` constructs the use-case ring itself, so an application that
only records and reads never names it. `NewWithService` takes one explicitly,
for a caller that already holds a `Service` or wraps it.

Three rows of that table are departures from the textbook — no DTOs, no driving
port, a deliberately thin output port. Each is argued on
[design decisions]({{ u_design }}); §5 below covers the thin port, because you
need its mechanics to read a backend.

## 3. The domain object is a log

The thing that surprises Clean readers most: there is no `Invoice`, no `Order`,
no aggregate root. The entity is a **record of edits**, and current state is
never stored — it is derived by replaying the log
([`kit/view`]({{ src }}/kit/view)).

That inverts the usual data flow. State is a projection, and any number of
projections can exist over the same log without migration, because none of them
is the source of truth.

The consequence to carry into §5: **every read is a replay**, which is why
`Snapshotter` and `TailReader` exist at all. Why a log rather than a state table
with an audit trail beside it is on
[design decisions]({{ u_design }}#the-domain-object-is-a-log-not-a-state-table);
the replay-and-snapshot mechanics are in
[concepts]({{ u_concepts }}#snapshotting).

## 4. The git decoder ring

The godoc talks in git, and half the port contract reads as noise without that
model. The call-by-call mapping — and the inversion the borrowed words hide —
is on [the git model]({{ u_model }}). Read it before the source if you have not.

The borrowing stops at one place, and it is the place worth being precise
about: chronicle does not merge or rebase. A commit's `Parent` is the writer's
assertion of the snapshot it built against, and the `Log` stores it verbatim —
two commits sharing a parent are a **fork**, a legal recorded fact, not an
error. Concurrency control, if a deployment wants writers serialized into one
chain, is the persistence layer's or the producer's job, never this library's;
`VerifyChain` accepts forks and multiple roots as ancestry, not corruption.
[Why merge, rebase, and branches stay out of
scope]({{ u_design }}#what-is-deliberately-absent).

## 5. Optional capabilities — the part with no analogue

Clean Architecture says: one repository interface, every method mandatory.
chronicle says: three mandatory methods, and everything else is *sniffed at
runtime*.

The mandatory port is `Log` — `AppendCommit`, `Commits`, `Head`. Four further
interfaces live in [`core/capability.go`]({{ src }}/core/capability.go) and are
entirely optional:

- `Indexer` — cross-document queries
- `TailReader` — read a document's chain from a cursor
- `Snapshotter` — one cached materialization per document
- `Deduper` — durable producer idempotency

`NewService` discovers them by type-asserting the `Log` and walking any
`Unwrap()` chain ([`core/service.go`]({{ src }}/core/service.go)) — which is why
`Service` looks like it is doing odd reflection on construction: it is asking
"does this backend also do X?" A backend that implements none simply has no
cross-document queries and no dedup.

Interchangeability is *proved*, not asserted:
[`core/conformance`]({{ src }}/core/conformance) is an executable spec — one
mandatory suite plus four opt-in ones — that every adapter runs in its own
tests.

Why three methods rather than eight, why sniffing rather than a constructor
flag, and why core keeps no fallback implementation are on
[design decisions]({{ u_design }}#three-mandatory-port-methods-everything-else-sniffed).
The runtime-strategy pattern itself is named in
[concepts]({{ u_concepts }}#capability-pattern-graceful-degradation).

## 6. The kit ring, in one screen

The application ring holds everything `core` refuses to know: what a document
is, how a path is written, what a change looks like to a human.

| Package | Import path | Job |
|---|---|---|
| `chroniclekit` | `kit/` | the facade — `RecordUpdate` / `RecordChanges` / `RecordPatch`, delegating reads |
| `chronicleschema` | `kit/schema` | the `Option`s a caller declares, and nothing else |
| `chroniclediff` | `kit/diff` | `Diff(before, after) []Change` |
| `chronicleview` | `kit/view` | replay: `Reconstruct`, `State`/`StateAt`, snapshots |
| `chronicleexplain` | `kit/explain` | read-time display decoration |
| `httpapi` | `kit/httpapi` | optional stdlib `http.Handler` |
| — | `kit/internal/docmodel` | path grammar, canonical JSON, `Apply` — shared machinery |
| — | `kit/internal/memlog` | in-memory `Log` for the kit's own tests |

**The import graph is a star.** `diff` and `explain` each import `schema` +
`docmodel` + `core`; `view` imports only `docmodel` + `core`, skipping
`schema`. None imports another. A new shared helper goes in `docmodel` if the
kit's packages share it, in `schema` only if a caller declares it.

How to use any of it is on [the kit page]({{ u_kit }}). Why it is a star, why
`schema` and `docmodel` are separate, and why there are three write methods are
on [design decisions]({{ u_design }}).

## Where to go next

- **To use it** — [the kit]({{ u_kit }}) is what to call, and
  [reference]({{ '/documentation/reference/' | relative_url }}) is the exact
  contract of each call.
- **To judge or change it** — [design decisions]({{ u_design }}) argues every
  choice on this page and states what it cost.
- **To read the source** —
  [contributing]({{ '/documentation/contributing/' | relative_url }}) has the
  file-by-file order, and the invariants a change must not break.
