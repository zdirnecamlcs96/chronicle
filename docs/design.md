---
title: Design decisions
permalink: /documentation/design/
eyebrow: design
source: design.md
summary: >-
  Why the library is shaped the way it is — one decision per section, each with
  the alternative that was rejected and what the choice costs. Nothing here is
  needed to use chronicle; read it to judge it, extend it, or argue with it.
---
{%- assign src = site.repo | append: '/blob/main' -%}
{%- assign u_architecture = '/documentation/architecture/' | relative_url -%}
{%- assign u_kit = '/documentation/kit/' | relative_url -%}
{%- assign u_model = '/documentation/model/' | relative_url -%}
{%- assign u_concepts = '/documentation/concepts/' | relative_url -%}

This page holds the *why*. [Architecture]({{ u_architecture }}) is the map —
what lives where. [The kit]({{ u_kit }}) is the how — what to call. Neither
repeats the reasoning below, so if a decision here looks arbitrary in the code,
this is the page that answers it.

Every entry has the same shape: **the decision**, the alternative that was
rejected, and what it costs. A design doc that lists only benefits is a sales
page.

---

## The core depends on nothing

[`core/go.mod`]({{ src }}/core/go.mod) has no `require` block at all. Not a lint
rule, not a convention — the domain ring cannot import an adapter because the
compiler will not let it.

**Rejected: one module, folder discipline.** A folder convention is enforced by
review. Every Clean codebase that has rotted, rotted the same way — someone
imported infrastructure from the domain "just this once", and the linter was
configured after the fact. Here that import does not compile, and nobody has to
remember why.

The second reason is the consumer's `go.sum`. A single module would put the
MySQL and ClickHouse drivers in the dependency tree of anyone using the
in-memory backend. Splitting by module makes "pick one backend" mean pick one
*driver*.

**Cost.** Releases are two-phase. `core/vX.Y.Z` must be tagged and pushed before
`kit` and the adapters can require it, so a change spanning rings ships as two
commits and two tags, never one.

## The core ships no transport, and no document model

Everything opinionated — what a document is, how a path is written, what a
change looks like to a human — lives one ring out in `kit`, which is optional.

**Rejected: batteries in the core.** It would make the common case shorter by
one import. What it would cost is that the domain acquires opinions it can never
retract: a path grammar in `core.Change.Path` becomes a storage format, and a
storage format is a migration to change. With the split, being wrong about a
document model is a `kit` release.

This is why `core.Change.Path` stays an **opaque string**. `core` must never
learn to parse it.

## `Change` crosses every ring unchanged

There are no DTOs and no mappers. The struct a producer builds is the struct the
adapter writes.

**Rejected: per-boundary DTOs.** The textbook maps entities at every boundary so
layers can evolve independently. Here that would break the product. `Commit.ID`
is a SHA-256 over the parent id, the message, and the canonical JSON of the
changes — a mapping layer that reordered a field or normalized a number would
change the hash and void the tamper-evidence. The absence of DTOs is what makes
the integrity claim checkable end to end: the bytes a producer handed over are
the bytes that were hashed.

**Cost.** `Change` is a published wire format from the first tag. Its JSON shape
cannot change without breaking every stored commit.

## There is no driving port

Transport is the consumer's job. [`kit/httpapi`]({{ src }}/kit/httpapi) is a
mountable example, not a layer you pass through.

**Rejected: an input-port interface.** It would have exactly one implementation
and no second caller — an abstraction that costs more than it returns. REST,
gRPC, a queue consumer, and a CLI differ enough that a shared interface
collapses to `any`.

**Cost.** Anyone not using Go, or not using `net/http`, writes their own
transport. There is no client SDK in any language and no hosted server.

## The domain object is a log, not a state table

There is no `Invoice`, no `Order`, no aggregate root. The entity is a record of
edits; current state is derived by replaying it
([`kit/view`]({{ src }}/kit/view)).

**Rejected: current state plus an audit trail beside it.** Two writes, two
things that can disagree, and the history is the copy nobody validates. Here
there is one write and state is a function of it — they cannot drift, because
there is only one of them.

The property that pays for it shows up in collaborative edits. `Commit.Changes`
is an **ordered list of operations**, not a snapshot of the result, so a commit
in which bob set the budget to 1500 and dave then overrode it to 1800 records
*both*. Replay converges on 1800; the audit still shows bob. A state table keeps
only the winner, and diffing two converged states reports a single 1000 → 1800
change with bob nowhere in it.

**Cost.** Every read is a replay. That is why `Snapshotter` and `TailReader`
exist at all — the model is only affordable if a replay can start somewhere
other than the root. It is also why there is no way to ask "which documents have
status = open" without an index; `Indexer` is the same admission.

## Git's vocabulary, borrowed and then broken

Commit, parent, head, chain, fork. The words arrive with their invariants
already understood: a parent pointer means order, a hash means content
addressing, and two commits sharing a parent is a fork.

**Rejected: neutral names** — entry, predecessor, tip. They would teach the same
ideas from scratch. The [git model]({{ u_model }}) page is the call-by-call
mapping, and states the inversion the borrowed words hide.

**Where the borrowing stops**, and it is the part that matters: chronicle
learns from git without copying it. A commit's parent is the snapshot the
writer built against — `RecordPatch` anchors it to the head its state read
reached, `WithParent` lets any caller assert it — and the Log stores it
verbatim. Two writers racing the same document record a **fork**: two honest
facts, both kept, folded last-write-wins in arrival order at read time. What
is NOT borrowed is everything after the fork — no branches, no refs, no merge;
`VerifyChain` checks ancestry (every parent exists, every hash matches), not
linearity. Concurrency control is not this library's job: serializing writers
belongs to the persistence layer or the producer, and pretending otherwise is
how a changelog starts recording state it never saw.

## Three mandatory port methods, everything else sniffed

`Log` is `AppendCommit`, `Commits`, `Head`. `Indexer`, `TailReader`,
`Snapshotter`, and `Deduper` live in
[`core/capability.go`]({{ src }}/core/capability.go) and are optional;
`NewService` discovers them by type-asserting the `Log` and walking any
`Unwrap()` chain.

**Rejected: one fat interface.** Eight mandatory methods would force every
backend to implement snapshots, dedup, cursors, and cross-document queries
before storing a single commit — and most would satisfy the signature with a
stub that lies. Three methods is a bar a new backend clears in an afternoon, and
an unimplemented capability is *absent* rather than faked.

The load-bearing half is that core keeps **no fallback implementation**. A
missing `Deduper` means idempotency genuinely does not happen — not that it
happens in memory and silently stops working across restarts.

**Rejected: constructor flags** — `NewService(log, WithSnapshots(true))` lets a
caller claim a capability the backend does not have. Type assertion asks the
only authority that can answer. `Unwrap()` is walked so a decorated `Log`
(fan-out, metrics, tracing) does not hide the capabilities of what it wraps.

**Cost.** Capability discovery happens once, at construction, via reflection-ish
type assertions — which is why `Service` looks like it is doing something odd
there.

## The contract ships as a test suite

[`core/conformance`]({{ src }}/core/conformance) is an executable spec: one
mandatory suite plus four opt-in ones, run by every adapter in its own tests.

**Rejected: a written contract.** Prose drifts from the code that implements it,
and each adapter re-interprets it slightly differently. An executable one cannot
drift, and it is what makes "write your own backend" an honest invitation rather
than an aspiration.

## The kit is a star, not a stack

`diff` and `explain` each import `schema`, `internal/docmodel`, and `core`;
`view` imports only `internal/docmodel` and `core`, skipping `schema` — and
none imports another.

**Rejected: layering them.** Each leaf is independently useful. A binary that
only renders history should not link the snapshot and cursor machinery; a
service that only records should not carry the display decorator. Stacking would
make every consumer pay for all of them.

**Cost.** Anything shared has to be pushed *down* into `schema` or `docmodel`
rather than borrowed sideways. That is the rule to keep when adding a helper.

## `schema` is vocabulary; `docmodel` is machinery

`chronicleschema` holds **only what a caller declares**. Path splitting,
canonical JSON, and `Apply` live in `kit/internal/docmodel`.

**Rejected: one package.** They were one. Exposing the grammar made the
vocabulary look twice as large as it is and invited callers to depend on rules
that are not a promise. The split is the sharpest boundary in the kit: if a
caller sets it, it goes in `schema`; if the kit's own packages share it, it goes
in `docmodel`.

## Write-side options are permanent; read-side ones are free

The axis that matters is not read/write in the CRUD sense — it is whether an
option changes what gets **recorded**.

| | Write side | Read side |
|---|---|---|
| Options | `WithArrayKeys`, `WithIdentityFields`, `WithStrictIdentity`, `WithValueTypes` | all but `WithStrictIdentity`, plus `WithLabels`, `WithNameFields`, `WithNames`, `WithIgnoredFields` |
| Output | `[]Change`, hash-sealed into a commit | rows and value trees, stored nowhere |
| Getting it wrong | **permanent** — no later option re-keys sealed history | free — change it, all history re-renders |

That asymmetry decides two things that otherwise look inconsistent.

**Display metadata is never stored inside the seal.** Not because storage is
expensive: the change JSON is the commit-hash preimage, so anything added to it
could never be backfilled, and history written before a schema existed would
render blind forever. Decoration is derived on read so yesterday's commits
benefit from today's labels.

The one thing read-time derivation cannot do is resolve a reference whose
entity is gone: an id whose item was deleted has no name to look up, and a
renamed item resolves to its *latest* name, not the name at change time. For
that, `chroniclekit.WithReadable()` opts a writer into a per-commit readable
sidecar — display rows frozen at seal time (`kit/explain.Readable`), stored
via the optional `Annotator` capability *outside* the hash seal. It is a
non-authoritative projection: deleting it is always safe, readers overlay only
its name-resolution fields (labels and trails still re-render live), and
commits without one decorate exactly as before.

**`WithStrictIdentity` exists** because the default failure is silent. Forget to
declare array identity and the write succeeds, the history looks plausible, and
the tests stay green — and no later declaration can re-key the commits. Strict
mode buys a loud failure at the only moment the mistake is still repairable. It
stays opt-in because positional pairing is the *right* answer where order is the
data; strict mode only refuses to choose it by accident.

## Three write methods, not one

They differ by what the caller already holds, and each exists because the others
would record the wrong thing:

| | Caller holds | Exists because |
|---|---|---|
| `RecordUpdate` | two documents | the CRUD default — diff and seal |
| `RecordChanges` | the operations | diffing converged states loses the operations that lost; a collaborative edit must record all of them |
| `RecordPatch` | an RFC 6902 patch | a patch is forward-only, so sealing it directly records changes with no before-values, permanently |

## What is deliberately absent

Reading the absences is faster than reading the code, and each is a decision
rather than a backlog item:

- **No merge machinery.** Forks are recorded honestly — two commits sharing a
  parent are two facts, folded last-write-wins in arrival order at read — but
  there are no branches, refs, or merges to manage. Verification checks
  ancestry (`ErrMissingParent` when a claimed base was never stored), not
  linearity; tamper-evidence rides on every commit's hash covering its parent.
- **No CRDT or conflict resolution.** Deciding *order* is a different domain.
  chronicle keeps the audit trail once something else has decided — which is why
  `RecordChanges` takes an ordered operation list.
- **No query language.** `Indexer` is the whole answer, and it is optional.
  Anything richer belongs in a projection built over the log.
- **No formatting.** The kit emits structure — slices, names, canonical scalars,
  flags — never joined strings, sentences, or colors. A CLI, a web app, and an
  email digest render the same rows differently, and the library never limits
  that.
- **No client SDK, no server.** `kit/httpapi` is a mountable example, not a
  product.
- **No auth anywhere.** Authentication, authorization, rate limiting, and quotas
  belong to the mounting application, in every ring.

## The guarantee boundary: best-effort now, guaranteed later

chronicle's trajectory is the one most distributed systems take: make it work,
capture enough to debug failures, raise the guarantees when the business
demands it. An audit system that promises 100% capture from day one becomes a
second distributed-transaction system — it has to reach into every write path
or own the persistence layer's change stream, and that complexity arrives
before any history exists to justify it. The boundary is stated here so
neither side gets mistaken for the other.

**Current version: a best-effort audit trail with tamper detection.**
Producers record what they saw; a crash between the document write and the
record call loses that event. What cannot happen *silently* is corruption of
what was recorded — even with events missing, the hash chain still
distinguishes three very different failure modes:

- *"the system never recorded this event"* — a gap. No commit explains a
  state change; visible when replayed state disagrees with the document of
  record.
- *"someone modified a recorded event"* — tampering. Content no longer
  matches its hash: `ErrHashMismatch` / `ErrAuthorsMismatch`.
- *"two independent histories exist"* — divergence. A fork (two commits
  sharing a parent), or `ErrMissingParent` when a claimed base was never
  stored.

Forks are the deliberate trade-off that keeps this version simple:
implement-first, observability-first. Nothing blocks, nothing lies — a race is
recorded as a race, and the failure modes above stay tellable apart.

**Future version: a guaranteed audit trail with durable event capture.** The
upgrade path is capture at the layer that already serializes writes, not more
machinery here: consume the persistence layer's change stream — CouchDB
`_changes`, MySQL binlog CDC (Debezium and kin) — and feed it to
`RecordUpdate`. The stream's ordering and durability make the log complete,
per-document ordered, and crash-proof, while chronicle stays exactly what it
is: the tamper-evident record of what the stream delivered. Roughly in order
of need:

1. a change-stream recorder example (CouchDB `_changes` first): keep the
   last-seen document per ID, diff each new revision against it, record;
2. cursor persistence, so a restarted recorder resumes from the stream's own
   sequence token without gaps;
3. reconciliation tooling: diff replayed state against the document of record,
   turning "never recorded" from silent into measurable;
4. optionally, transactional co-capture where the store can host the log in
   the same transaction — the SQL adapter already lives in a database, and a
   same-transaction append is the strongest guarantee of all, no stream
   needed.

---

The patterns and algorithms behind these choices — append-only log, immutable
events, hash chain, event sourcing, snapshotting, forked lineage, idempotent
consumer, LCA — are named and located in [concepts]({{ u_concepts }}).
