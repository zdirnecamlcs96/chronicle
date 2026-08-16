---
title: Contributing to chronicle
permalink: /documentation/contributing/
eyebrow: contributing
source: CONTRIBUTING.md
summary: >-
  How the codebase is shaped and the design patterns that hold it together, so
  your changes fit the architecture and preserve its invariants.
---

Read **The mental model** and **Core design patterns** first — they define the
rules. **Writing an adapter** and **The kit layer** are the two most common
extension points. **Conventions & gotchas** is the "do NOT" checklist.

## Before you start

Two pages are assumed here rather than repeated:
[the git model]({{ '/documentation/model/' | relative_url }}) for the vocabulary
(document as branch, `Change` as a diff line, `Log` as the repository) and
[concepts]({{ '/documentation/concepts/' | relative_url }}) for why each pattern
was chosen. This page is what a change must not break, and where each file
sits.

## Repository layout

chronicle is **not one module**. It is a Go workspace (`go.work`) of one module
per concern:

```
core/                 package changelog — the contract + porcelain (stdlib only)
  conformance/        the executable Log contract — a package WITHIN core (not its own module)
adapters/memory/      in-memory reference/POC backend (no driver)
adapters/sql/         MySQL backend (go-sql-driver/mysql)
adapters/clickhouse/  ClickHouse backend (clickhouse-go/v2)
kit/                  chroniclekit — batteries-included layer (stdlib + core)
  schema/             chronicleschema — the shared document model
  diff/               chroniclediff — Diff
  view/               chronicleview — Reconstruct / State / snapshots
  explain/            chronicleexplain — read-time display decoration
  httpapi/            optional stdlib http.Handler over a Service
  internal/docmodel/  path grammar, canonical JSON, Apply — shared, not caller vocabulary
  internal/memlog/    in-memory Log for the kit's own tests
```

There are exactly **five modules** (`core`, the three adapters, `kit`).
`core/conformance` is a stdlib-only package inside the `core` module, and the
kit's five packages (`chroniclekit`, `schema`, `diff`, `view`, `explain`) plus
`httpapi` and the two internal ones are packages within the `kit` module — they
carry no dependencies of their own, so there is nothing to isolate behind a
module boundary.

Rules that the layout enforces:

- **The repo root is not a module.** Build/test by enumerating module paths
  (see **Repo commands**). Don't add a root `go.mod`.
- **`core` is standard-library only.** `core/go.mod` has no `require` block, and
  `core/conformance` is a stdlib-only package within the `core` module — so neither
  adds a dependency. A consumer can depend on the contract with zero driver /
  supply-chain cost.
- **Dependencies point one way: `adapter → core` and `kit → core`, never the
  reverse.** `core` imports no adapter and no kit. This dependency-inversion rule
  is what makes the library database-agnostic; the workspace makes it physically
  enforceable.
- **Drivers are quarantined in adapters.** Each durable adapter carries its own
  driver in its own `go.mod`. Using the memory backend never pulls the ClickHouse
  driver.
- **Modules version independently** with Go subdirectory tags (`core/vX.Y.Z`,
  `adapters/sql/vX.Y.Z`, `kit/vX.Y.Z`, …); an adapter/kit requires a tagged `core`.

## Reading order

Read the source in this order and each file explains the next:

1. `core/doc.go` — the git mental model, stated up front
2. `core/change.go` → `core/commit.go` — the entities. Read `computeID`; that is
   the whole integrity story
3. `core/log.go` — the port. Three methods. Stop and absorb
4. `core/recorder.go` — stage → seal → append
5. `core/service.go` — seal, dedup, capability discovery
6. `adapters/memory` — the simplest port implementation; read it whole
7. `core/conformance` — what a backend must obey
8. `kit/view` — replay. Where "state" finally appears
9. `core/capability.go` — last, because it has no Clean Architecture analogue

Then, if you are working in the kit rather than on a backend:

10. `kit/schema/options.go` — the whole caller vocabulary, and which half of it
    is permanent
11. `kit/diff` — read `array` and `keyedArray`; element identity is the one
    decision a later release cannot undo
12. `kit/kit.go` — the three write paths and why each exists

## Core design patterns

Each is stated as **what / how / why / the invariant you must preserve.**

### 1. Porcelain / plumbing / facade — Recorder vs Log vs Service

**What.** Three layers mirror git. **Log** is the storage port (plumbing).
**Recorder** is the staging + `git commit` porcelain, fixed to ONE document.
**Service** is the cross-document, in-process facade over any Log.

**How.** `Recorder` (`core/recorder.go`) holds `(docID, log, clock, pending)` and
every method operates on its one `docID`; it depends only on the `Log` port.
`Append` stages a Change (stamping `At` from the Recorder's clock); `Commit` reads
`Head`, hash-chains, appends, and on any error **restores the pending buffer** so
nothing is lost. `Service` (`core/service.go`) is the only layer spanning
documents; it owns no buffer and builds a fresh Recorder per `Seal`.

**Invariant.** Keep the Recorder single-document — never add a `docID` parameter to
`Append`/`Commit`. Cross-document behavior belongs on the Service. Never make
`core` import `net/http` or any transport.

### 2. Ports & Adapters — the narrow 3-method Log seam

**What.** Every backend plugs in through one small interface, `Log`, with exactly
three methods (`core/log.go`): `AppendCommit`, `Commits` (newest-first; `limit<=0`
= all), `Head` (`""` when empty).

**Why.** A tiny seam keeps each backend's mandatory surface minimal and lets all
the porcelain (chaining, hashing, idempotency) live once in `core`.

**Invariant.** Keep `Log` at exactly these three methods. Cross-document queries
and dedup are **optional capabilities** (pattern 4), not Log methods.

### 3. Content-addressing & hash integrity

**What.** Why the chain is hashed, and what content-addressing does and does not
buy, is [concepts]({{ '/documentation/concepts/' | relative_url }}).

**How.** `computeID` (`core/commit.go`) writes a fixed, unframed version tag
(`commitPreimageV1`, `"chronicle.commit.v1\n"`), then hashes three
**length-framed** fields in a fixed order via `writeField` (each prefixed by
its byte length as 8 big-endian bytes). `TestComputeID_CanonicalPreimageFormat`
pins the exact byte layout; `TestComputeID_FieldsAreUnambiguous` proves the
framing blocks a colliding re-split; `TestComputeID_Golden` pins exact hex IDs
for fixed inputs.

**Invariant.** Treat the preimage (version tag, field order, length-framing,
what is/isn't included) as a **permanent on-disk format**. Changing it
reshuffles every commit ID and breaks every stored chain across all adapters.
Do NOT add fields to the hash, reorder the framing, drop the length prefixes,
or swap the hash/JSON encoders — bump the version tag instead.

### 4. Optional capabilities via type-assertion + Unwrap chain

**What.** Cross-document queries (`Indexer`), producer idempotency (`Deduper`),
cursor reads of a document's chain (`TailReader`), and a per-document snapshot
cache (`Snapshotter`) are **optional** interfaces a backend MAY implement
(`core/capability.go`).

**How.** `NewService` (`core/service.go`) type-asserts the Log for `Indexer` and
`Deduper`, walking any `Unwrap() Log` chain to find them, and keeps **no
fallback**: a backend implementing neither simply has no cross-document queries
and no dedup. The kit's `New` walks the same chain (starting from the Service's
own `Unwrap() Log`) for `TailReader` + `Snapshotter`; with both present, `State`
reads snapshot + tail instead of replaying the full history.

**Invariant.** New optional backend behavior goes behind a capability interface
detected this way — not bolted onto the mandatory `Log` port.

### 5. Functional options

**What / how.** Call-site config is variadic functional options:
`WithMessage` / `WithIdempotencyKey` (`core/recorder.go`, `core/service.go`).
They are additive and backward-compatible — `rec.Commit(ctx)` keeps working.

**Invariant.** Add new knobs as options, not as new required parameters.

### 6. Forks are legal — no conflict, no retry

**What.** Concurrent same-document appends are NOT reconciled by this library.
A commit's `Parent` is the writer's assertion of the snapshot it built against
(`Recorder.WithParent`, `Service`'s `WithSealParent`); the `Log` stores it
verbatim. Two commits sharing a parent are a **fork** — both land, no error.

**How.** `Recorder.Commit` defaults `Parent` to `Head` at commit time unless
`WithParent` overrides it; `Service.Seal` forwards the same choice via
`WithSealParent`. `VerifyChain` (`core/verify.go`) accepts forks and multiple
roots as legitimate ancestry, not corruption.

**Invariant.** Concurrency control — serializing writers, or reconciling a
fork — is the persistence layer's or the producer's job, never `core`'s. A
backend must never invent a conflict error to reject a fork; prove fork
tolerance with `RunLogConformance`'s `ForkAppend` subtest.

### 7. Producer idempotency — Deduper

**What / how.** With `WithIdempotencyKey`, `Seal` looks up the key via `Deduper`
and returns the already-sealed commit on a replay, so at-least-once delivery seals
exactly one commit. Keys are scoped per document. `MarkSeen` is best-effort (a
failure degrades to at-least-once; the commit is already durable).

**Invariant.** Keys are per-document — never resolve a key across documents (proved
by `RunDeduperConformance`).

### 8. Injectable clock

`Recorder.WithClock` replaces the time source for deterministic tests/replay.
Use it instead of reading the wall clock in tests.

## Writing an adapter

The core declares the contract; an adapter follows it (the interface *is* the
contract — Go has no base class):

1. Implement `changelog.Log` (the three mandatory methods).
2. Optionally implement `changelog.Indexer` and/or `changelog.Deduper`.
3. Assert at compile time: `var _ changelog.Log = (*MyLog)(nil)`.
4. Prove behavior with `conformance.RunLogConformance` (and the opt-in suites
   where applicable).

**Encode correctness as schema constraints** — see `adapters/sql/schema.go`:
`PRIMARY KEY (doc_id, seq)` for ordering and as the lock target; `UNIQUE
(doc_id, id)` so a duplicate identical commit re-append is an idempotent
no-op; and a plain `KEY idx_doc_parent (doc_id, parent)` — forks (two commits
sharing a parent) are legal recorded facts, so nothing constrains them, the
index only keeps parent lookups fast. The SQL adapter's `FOR UPDATE` on the
head row serializes only seq assignment, never what a writer's `Parent`
claims. `Migrate` is idempotent (`CREATE TABLE IF NOT EXISTS`) and auto-swaps
an existing table's legacy `(doc_id, parent)` UNIQUE constraint for the plain
`idx_doc_parent` index.

**Consistency models differ, the contract doesn't.** `adapters/sql` is
transactional (`FOR UPDATE` serializes seq assignment per document);
`adapters/clickhouse` is eventual (`ReplacingMergeTree` + `FINAL`, ordered by
`(doc_id, at, id)`, no locks). Both pass the same mandatory suite, `ForkAppend`
included — a fork is a recorded fact on either backend, not a defect to
prevent. `adapters/memory` is reference/POC only: it stores structs in a map
and must never be used in production.

## The kit layer — chroniclekit

`kit/` is the **batteries-included layer over `core`** — the "produce /
transport / render" work that `core` deliberately omits. It imports **only
`core` (+ stdlib)** and is adapter-agnostic (construct it over any
`changelog.Service`); `core` never imports it.

The kit is also where everything `core` refuses to know lives — a document, a
path grammar, and comparison:

- **The declared vocabulary** (`kit/schema`, package `chronicleschema`): the
  `Option`s a caller uses to declare its document's shape, plus the
  create/put/delete kinds re-exported for convenience. Everything below imports
  it, and nothing else of the kit's. It holds **only what a caller sets** —
  the dotted path grammar, canonical JSON, and `Apply` live in
  `kit/internal/docmodel`, because they are machinery the kit's packages share
  with each other, not vocabulary anyone declares.
- **Change production** (`kit/diff`, package `chroniclediff`):
  `Diff(before, after) []Change` — JSON-normalizes both sides and deep-diffs
  into create/put/delete changes.
- **Read / render + snapshot** (`kit/view`, package `chronicleview`):
  `Reconstruct`/`State`/`StateAt` replay changes into document state;
  `CommitSnapshot` derives a per-commit snapshot **on read** — the before-state
  of the `lcaPath` (segment-wise lowest common ancestor) of the commit's changed
  paths. Nothing is stored in `core` or any adapter for this.
- **Display decoration** (`kit/explain`, package `chronicleexplain`): `Explain`
  replays a chain into display-ready rows. Derived at read time; nothing
  display-shaped is ever stored.
- **Seal facade** (`kit/kit.go`, package `chroniclekit`): `RecordUpdate`
  (diff → Seal), `RecordChanges`, `RecordPatch` (state → apply → diff → Seal),
  and the three read methods delegating to a `chronicleview.Reader`.
- **JSON-Patch interop** (`kit/jsonpatch.go`): `FromChanges`/`ToChanges` convert
  to/from RFC 6902, mapping the kit's dotted path to/from RFC 6901 JSON Pointer.
  Both are pure functions of their argument, which is why `ToChanges` cannot
  fill `From` — nothing at that call site has the document. `RecordPatch` is the
  write path precisely because it does.
- **HTTP transport** (`kit/httpapi`): a stdlib `http.Handler` over a `Service`.

**The import graph is a star.** `diff`, `view`, and `explain` each import
`schema` + `internal/docmodel` + `core` and nothing else of the kit's; none
imports another. That is why `Apply` lives in `docmodel` rather than `view` —
`explain` needs to replay a change, and putting `Apply` in `view` would drag the
`Snapshotter`/`TailReader` machinery into any binary that only renders history.
Keep it that way: a new shared helper belongs in `docmodel` if the kit's
packages share it, in `schema` only if a caller declares it.

**Invariant.** The **path grammar is a kit concern only** — `core.Change.Path`
stays an opaque string; don't teach `core` to parse it. Keep the kit importing
only `core`. Any new "produce changes from state" or "render state" feature
belongs here, not in `core`.

### Identity is a write-side declaration

**What.** Array element identity — `WithArrayKeys`, `WithIdentityFields` — is
the one part of the option vocabulary that changes what is **recorded**. It
decides whether an array edit seals as `lines.0.qty: 1→3` against a stable
element or as a positional rewrite.

**How.** `Config.IdentityKey` (`kit/schema/options.go`) walks configured key →
each `WithIdentityFields` entry in order → positional. Write and read call the
same method and differ only in how many revisions they pass: `Diff` passes
before **and** after, because its result shapes the `[]Change` that `Seal`
hashes; `Explain` passes the one revision in hand, so it reads an array back
the way `Diff` recorded it.

**Why.** Every other option is read-side: labels, name fields, names, ignored
fields all decorate at read time, so a wrong one is fixed by re-rendering.
Identity is not — commits are hash-sealed, and no later declaration can re-key
history that was recorded positionally.

**Invariant.** The kit **guesses no field names** for identity; there is no
default and there must not be one. Give the same declaration to both sides, and
treat a shipped identity declaration as permanent — changing it splits a
document's history into commits recorded under two different pairings.

**The guard.** `WithStrictIdentity` turns the last hop of that chain into
`chroniclediff.ErrNoIdentity` for arrays of objects. It exists because the
default failure is *silent*: forget the declaration and the write succeeds, the
history looks plausible, and the tests stay green. Keep it opt-in — positional
pairing is the right answer where order is the data, so this can never become
the default — and keep the check where it is, in `differ.array` at the point the
chain runs out. Anywhere earlier and it would reject arrays that turn out to be
keyed; anywhere later and the commit is already sealed.

## Testing & the conformance contract

A backend is "correct" when it passes the suite (`core/conformance`):

- `RunLogConformance` (mandatory): empty head/commits, append→head, parent
  chaining, hash re-verification of the stored chain plus the `Authors`
  recomputation (`changelog.Verify`), newest-first order, limit, per-doc
  isolation, two commits sharing a parent both landing with `Head` as the
  latest arrival (`ForkAppend`), context cancellation.
- `RunDeduperConformance` (opt-in): idempotency keys are scoped per document.
- `RunTailReaderConformance` (opt-in): `CommitsAfter` cursor reads — oldest
  first, `""` = from root, unknown/foreign cursor → `ErrNoSuchCommit`.
- `RunSnapshotterConformance` (opt-in): one snapshot per document, latest write
  wins, bytes round-trip, per-doc isolation.

The suite takes a `NewLog` factory (fresh Log + teardown per subtest) and seals
with a **monotonic clock** so timestamp-ordered backends and seq-ordered backends
both return deterministic order. Real-database adapter tests are gated behind
`//go:build integration`. Follow TDD (write the failing test first); keep code
`gofmt`-clean.

## Conventions & gotchas (the "do NOT" list)

- Do NOT put `At`/`Authors` (or anything else) into the commit hash, or change the
  `computeID` preimage format — it's a permanent on-disk format.
- Do NOT make `core` import an adapter, the kit, or `net/http`. Dependencies flow
  inward only.
- Do NOT add methods to the `Log` port for cross-document/dedup features — use a
  capability interface.
- Do NOT teach `core` a `Path` grammar — that lives in the kit.
- Do NOT use `adapters/memory` in production (reference/POC; it is not durable — state is lost on restart).
- Prefer functional options over new required parameters; use `WithClock` in tests.

## Repo commands

```sh
# go.work spans all modules; the repo root is not itself a module
go test ./core/... ./core/conformance/...
( cd kit && go test ./... )
( cd adapters/memory && go test ./... )
( cd adapters/sql && go test ./... )           # unit; integration is build-tagged
( cd adapters/clickhouse && go test ./... )

# real-DB adapter tests:
#   CHANGELOG_SQL_TEST_DSN=… go test -tags integration ./adapters/sql/...

# build everything
go build ./core/... && ( cd kit && go build ./... ) \
  && ( cd adapters/memory && go build ./... ) \
  && ( cd adapters/sql && go build ./... ) \
  && ( cd adapters/clickhouse && go build ./... )
```
