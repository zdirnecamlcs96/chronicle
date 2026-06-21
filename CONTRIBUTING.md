# Contributing to chronicle

This guide explains how the codebase is shaped and the design patterns that hold
it together, so your changes fit the architecture and preserve its invariants.
Read **The mental model** and **Core design patterns** first — they define the
rules. **Writing an adapter** and **The kit layer** are the two most common
extension points. **Conventions & gotchas** is the "do NOT" checklist.

## The mental model: it's git

chronicle is intentionally git-shaped, and the vocabulary is deliberate:

- A **document** is a branch — its own independent linear history.
- A **Change** (`core/change.go`) is one line of a diff: a single recorded edit
  (`Path`, `Kind`, `From`, `To`, `Actor`).
- A **Commit** (`core/commit.go`) is an immutable, content-addressed bundle of
  Changes, hash-chained to its parent. Its `ID` is a SHA-256 content hash — like a
  git SHA, with one deliberate divergence (below).
- The **Log** (`core/log.go`) is the repository — the pluggable storage port
  (`AppendCommit` / `Commits` / `Head`).
- The **Recorder** (`core/recorder.go`) is git's porcelain: staging + `git commit`,
  bound to a single document.
- The **Service** (`core/service.go`) is the in-process, cross-document facade
  (`Seal`, `Commits`, `AllCommits`, `Get`) — plus producer idempotency.

See `core/MODEL.md` for a side-by-side with git.

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
```

There are exactly **five modules** (`core`, the three adapters, `kit`).
`core/conformance` is a stdlib-only package inside the `core` module.

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
the porcelain (chaining, hashing, retry, idempotency) live once in `core`.

**Invariant.** Keep `Log` at exactly these three methods. Cross-document queries
and dedup are **optional capabilities** (pattern 4), not Log methods.

### 3. Content-addressing & hash integrity

**What.** A Commit's `ID` is the SHA-256 of a fixed preimage over
`(parent, message, canonicalJSON(changes))`. Equal content ⇒ equal ID; any
difference changes it — a tamper-evident chain.

**How.** `computeID` (`core/commit.go`) hashes three **length-framed** fields in a
fixed order via `writeField` (each prefixed by its byte length as 8 big-endian
bytes). Deliberately UNLIKE git, the commit's `At` and `Authors` are **excluded**
from the hash, so the same logical change sealed by a different producer or at a
different time converges to one ID — which is what powers dedup (pattern 7).
`TestComputeID_CanonicalPreimageFormat` pins the exact byte layout;
`TestComputeID_FieldsAreUnambiguous` proves the framing blocks a colliding
re-split.

**Invariant.** Treat the preimage (field order, length-framing, what is/isn't
included) as a **permanent on-disk format**. Changing it reshuffles every commit
ID and breaks every stored chain across all adapters. Do NOT add fields to the
hash, reorder the framing, drop the length prefixes, or swap the hash/JSON
encoders.

### 4. Optional capabilities via type-assertion + Unwrap chain

**What.** Cross-document queries (`Indexer`) and producer idempotency (`Deduper`)
are **optional** interfaces a backend MAY implement (`core/capability.go`).

**How.** `NewService` (`core/service.go`) type-asserts the Log for `Indexer` and
`Deduper`, walking any `Unwrap() Log` chain to find them, and keeps **no
fallback**: a backend implementing neither simply has no cross-document queries
and no dedup.

**Invariant.** New optional backend behavior goes behind a capability interface
detected this way — not bolted onto the mandatory `Log` port.

### 5. Functional options

**What / how.** Call-site config is variadic functional options:
`WithMessage` / `WithIdempotencyKey` (`core/recorder.go`, `core/service.go`).
They are additive and backward-compatible — `rec.Commit(ctx)` keeps working.

**Invariant.** Add new knobs as options, not as new required parameters.

### 6. Optimistic concurrency — ErrParentConflict + retry

**What.** Concurrent same-document appends are handled optimistically. A backend
returns `ErrParentConflict` (`core/log.go`) when a commit's `Parent` no longer
matches `Head` (git's non-fast-forward).

**How.** `Service.Seal` retries up to `maxSealAttempts`: the Recorder restored the
pending changes, so each retry re-reads `Head` and re-chains/re-hashes.

**Invariant.** A transactional backend MUST surface `ErrParentConflict` (not a
driver-specific error) so the retry loop works; prove it with
`RunSerializableAppend`.

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
`PRIMARY KEY (doc_id, seq)` for ordering and as the lock target;
`UNIQUE (doc_id, id)`; and `UNIQUE (doc_id, parent)` — the anti-fork rule (at most
one child per parent, exactly one root per document). The SQL adapter serializes
appends (`FOR UPDATE`) and maps the unique-violation to `changelog.ErrParentConflict`.
`Migrate` is idempotent (`CREATE TABLE IF NOT EXISTS`).

**Consistency models differ, the contract doesn't.** `adapters/sql` is
transactional and fork-free (passes `RunSerializableAppend`). `adapters/clickhouse`
is eventual (`ReplacingMergeTree` + `FINAL`, ordered by `(doc_id, at, id)`) and cannot
pass `RunSerializableAppend` — it has no synchronous locks. `adapters/memory` is
reference/POC only: it stores structs in a map, can fork, and must never be used
in production.

## The kit layer — chroniclekit

`kit/` (package `chroniclekit`) is the **batteries-included layer over `core`** —
the "produce / transport / render" work that `core` deliberately omits. It imports
**only `core` (+ stdlib)** and is adapter-agnostic (construct it over any
`changelog.Service`); `core` never imports it.

The kit is also where everything `core` refuses to know lives — a document, a
path grammar, and comparison:

- **Change production** (`kit/diff.go`): `Diff(before, after) []Change` —
  JSON-normalizes both sides and deep-diffs into create/put/delete changes.
- **JSON-Patch interop** (`kit/jsonpatch.go`): `FromChanges`/`ToChanges` convert
  to/from RFC 6902, mapping the kit's dotted path to/from RFC 6901 JSON Pointer.
- **Read / render + snapshot** (`kit/view.go`): `Reconstruct`/`State`/`StateAt`
  replay changes into document state; `CommitSnapshot` derives a per-commit
  snapshot **on read** — the before-state of the `lcaPath` (segment-wise lowest
  common ancestor) of the commit's changed paths. Nothing is stored in `core` or
  any adapter for this.
- **Seal facade** (`kit/kit.go`): `RecordUpdate` (diff → Seal), `RecordChanges`.
- **HTTP transport** (`kit/httpapi`): a stdlib `http.Handler` over a `Service`.

**Invariant.** The **path grammar is a kit concern only** — `core.Change.Path`
stays an opaque string; don't teach `core` to parse it. Keep the kit importing
only `core`. Any new "produce changes from state" or "render state" feature
belongs here, not in `core`.

## Testing & the conformance contract

A backend is "correct" when it passes the suite (`core/conformance`):

- `RunLogConformance` (mandatory): empty head/commits, append→head, parent
  chaining, newest-first order, limit, per-doc isolation, context cancellation.
- `RunSerializableAppend` (opt-in): concurrent same-doc seals form one linear
  chain — transactional backends only.
- `RunDeduperConformance` (opt-in): idempotency keys are scoped per document.

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
- Do NOT use `adapters/memory` in production (reference/POC; it can fork).
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
