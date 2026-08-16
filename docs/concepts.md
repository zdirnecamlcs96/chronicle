---
title: Concepts
permalink: /documentation/concepts/
eyebrow: concepts
source: concepts.md
summary: >-
  The lookup table for names: what each pattern chronicle is built on is
  called, which file implements it, and the prior art it came from. Ordered
  roughly along the write → verify → read lifecycle.
---
{%- assign src = site.repo | append: '/blob/main' -%}
{%- assign u_design = '/documentation/design/' | relative_url -%}
{%- assign u_model = '/documentation/model/' | relative_url -%}

This page is the **lookup table**: what each pattern is called, which file
implements it, and the prior art it came from. Why any of it was chosen is on
[design decisions]({{ u_design }}); where the modules sit is on
[architecture]({{ '/documentation/architecture/' | relative_url }}).

## Append-only log, immutable events

The storage model in two terms: the log is **append-only** — commits are only
ever added, never updated or deleted; there is no edit or compaction API — and
each commit's changes are **immutable events**: facts about what happened,
sealed at write time. A correction is a new commit that changes the value
back, on the record, attributed — never a rewrite of the old one. Everything
else on this page (hash chain, replay, snapshots, forks) leans on these two
properties.

## Hash chain

Every commit's ID is `SHA-256(version tag, parent ID, message, changes)` — see
`computeID` in [`core/commit.go`]({{ src }}/core/commit.go). Because the
parent's ID is *inside* the hash, altering any historical commit changes every
ID after it: the log is tamper-evident, the same structure git and
blockchains use. A fixed leading tag domain-separates the commit preimage
from other hashed object types, and the remaining fields are length-framed
before hashing so no two different inputs can collide by concatenation. This
is what makes immutability *checkable* rather than promised.

The commit's own time and author metadata are deliberately excluded from the
hash, which is where chronicle diverges from git's SHA — that divergence, and
what it means for re-staging the same edit, is worked through on
[the git model]({{ u_model }}#the-one-deliberate-divergence).

## Content-addressed storage

An object's identifier is derived from its content rather than assigned: equal
`(parent, message, changes)` always name the same commit, and an ID can be
re-verified from content alone. Write-time dedup is NOT hash-based, though —
`Seal` restamps change timestamps into the payload, so a redelivered message
dedups to one commit through its idempotency key (`Service.Seal` + the
`Deduper` capability, [`core/service.go`]({{ src }}/core/service.go)), not
through its hash. (The SQL adapter does treat re-appending a byte-identical
commit — same document, same content hash — as a no-op.)

## Trusted anchor / checkpoint verification

Full verification ([`core/verify.go`]({{ src }}/core/verify.go) `Verify`) refetches and
rehashes the entire history — O(all commits), every run — and is the ancestry
authority: only the full history can decide that every commit's claimed parent
exists. The incremental form (`VerifyAfter`) content-checks just the commits
*after* a persisted cursor — hash and authors, O(commits since the last run) —
and returns the next cursor. Each commit's hash covers its parent, so content
tampering in the tail is caught; whether a parent *exists* is a question a
tail cannot answer (a legitimate fork's parent lies before any cursor), so
run the full `Verify` when ancestry is in doubt.

The trust contract is explicit — tampering at or before the cursor is
invisible to incremental verification, so the cursor is only as trustworthy
as where it is stored. Anchor in the same database guards against corruption
and casual tampering; against a hostile database owner the anchor (or a
signature over it) must live elsewhere.

*Why not a Merkle tree?* Trees buy O(log N) membership proofs — proving one
arbitrary record to a third party. "Is my linear history intact" doesn't need
that.

`VerifyAfter`'s anchor is per document; it says nothing about a document
whose entire history vanished, or another document the consumer never got
around to anchoring. **Inventory checkpoints** (`ComputeCheckpoint`/
`VerifyCheckpoint`, [`core/checkpoint.go`]({{ src }}/core/checkpoint.go))
close that gap: `ComputeCheckpoint` scans every document via `Indexer` into a
`Checkpoint` — one `DocState{Heads, Commits}` per document — and digests it
with the same length-framed, domain-separated SHA-256 construction as a
commit ID (`checkpointPreimageV1`), canonicalized (entries sorted by DocID,
each `Heads` sorted) so the digest doesn't depend on storage return order.
The digest commits to *both* heads and counts because they catch different
tampering: **Heads** catch a truncated or rewritten tail — a recorded tip
id no longer present anywhere in the document's current commits; **Commits**
catches interior deletion — a row quietly removed from the middle of the
chain, which leaves every head untouched and so would slip past a
heads-only check.

`VerifyCheckpoint` is deliberately not an equality check: chains legitimately
grow after a checkpoint, so additional commits on a checkpointed document,
and documents created since, are expected and do not fail it. What it does
flag, per document, is a checkpointed document now entirely absent
(`MissingDocs`), a recorded head no longer among the document's current
commit ids (`MissingHeads`), or a current commit count lower than recorded
(`ShrunkDocs`) — and it recomputes the checkpoint's own digest from its
`Inventory` first, so a doctored checkpoint (edited after the fact, digest
left stale) is caught before it is ever compared against the log.

The same trust contract as `VerifyAfter`'s anchor applies, just at the scope
of the whole inventory rather than one document: a checkpoint is only as
trustworthy as where it is kept. `Anchorer` is deliberately not a storage
capability — unlike `Indexer`/`TailReader`/etc., which a `Log` backend
implements, `Anchorer` is implemented by the *consumer*, because it must
write somewhere the database owner cannot reach (another organization's
store, WORM storage, even a printout). An anchor stored in the same database
it guards is decoration, not a checkpoint. Core ships the port only, no
implementation.

## Commit signing

Hash chaining and checkpoints answer whether the log was edited or truncated
after the fact; neither says who was allowed to write it. **Signing** answers
that: `Recorder.WithSigner`/`Service.WithSigner` Ed25519-sign a commit's `ID`
under a caller-chosen `keyID` label at seal time, storing `SigKeyID` and
`Signature` on the `Commit` itself, both outside the hash preimage — a
signature authenticates the hash, so it cannot also be part of what it
authenticates. Forging a validly-chained commit then needs the signing key,
not just database write access — the gap the threat table in
[README.md]({{ src }}/README.md) used to mark "not detected."

The signed message is the domain tag `chronicle.sig.v1\n` followed by the
commit's `ID` (`sigMessage` in
[`core/sign.go`]({{ src }}/core/sign.go)) — not the raw fields again. Because
`ID` already transitively covers `(Parent, Message, Changes)`, and `Parent`
covers everything before it, signing `ID` alone authenticates the commit's
full content and, through the parent chain, its entire ancestry.

`VerifySignatures` fetches a document's full history and classifies every
commit into exactly one of `Unsigned`/`Valid`/`Invalid`/`UnknownKey` — a
`SignatureReport`, report-don't-verdict like `CheckpointReport`: whether a
document's commits must all be signed is caller policy, so an unsigned or
invalid commit is never an error return.

Key distribution is entirely the caller's. `WithSigner` accepts any
`crypto.Signer` whose `Public()` is an `ed25519.PublicKey` (so a KMS/HSM-backed
signer works as well as a raw `ed25519.PrivateKey`; a different key type is
rejected at the first commit sealed under it); chronicle stores no private
keys and ships no PKI. A `KeyResolver` looks up the public key registered
under a commit's `SigKeyID` at verify time — rotation is minting a new
`keyID` and pointing `WithSigner` at it going forward, and old commits keep
verifying under whatever key their own `SigKeyID` still names.

Signing is **authoritative, not best-effort** — the opposite contract from
the [readable sidecar](#dual-audience-change-records) below. A configured
signer that fails to sign fails the whole `Commit`/`Seal` call, restoring the
staged changes like any other error: a silently-unsigned commit under a
configured signer would be forgery-shaped, so there is no silent-degrade path
here.

What signing does not cover: a commit's *absence*. A hostile database owner
can still delete a signed commit outright — that stays checkpoints +
`Anchorer`'s job, above, not signing's.

## Chain integrity vs log completeness

Everything above — `Verify`, `VerifyAfter`, checkpoints — answers one
question: was the log edited after the fact? None of it answers a different
one: does the log agree with reality? A write that never goes through
chronicle at all (a direct `UPDATE`, a migration script, a hotfix) leaves a
perfectly self-consistent hash chain that simply never mentions it — nothing
about *tamper-evidence* is violated, because nothing was tampered with.
`kit.Reconcile` closes that gap by diffing the log's replayed state against
the caller's live row; `kit.ReconcileSweep` does the same at the level of
whole documents, for a row deleted (or never written) outside chronicle
entirely. Both need the two checks side by side: chain integrity says the
history wasn't rewritten, log completeness says the history is the whole
story.

## Event sourcing

The storage philosophy: persist the *changes* (events), derive current state
by replaying them. `Reconstruct` in [`kit/view`]({{ src }}/kit/view) is the
replay; `State` is a *projection* (materialized view) of the event stream.
Core deliberately stores only the log — every read model is derived.

## Snapshotting

The classic event-sourcing companion pattern: cache the projection as of a
known commit, then replay only the events after it. The `Snapshotter`
capability ([`core/capability.go`]({{ src }}/core/capability.go)) stores one opaque
snapshot per document; `State`, `StateAt`, and `CommitSnapshot` in
[`kit/view`]({{ src }}/kit/view) all use it as a fast path, turning reads from
O(all commits) into O(commits since snapshot). The cost is *amortized*: an
occasional full replay primes the cache, subsequent reads are cheap. The
snapshot is a pure cache — deleting it is always safe, readers fall back to
full replay.

Historical reads (`StateAt`, `CommitSnapshot`) never refresh the snapshot: a
read of the past must not move the HEAD cache.

## Dual-audience change records

One stored history, two audiences. The record stays machine-shaped — dotted
paths, canonical-JSON scalars — because change JSON is the commit-hash
preimage: stored display metadata could never be backfilled, and old history
would render display-blind forever. Instead, `Explain` in
[`kit/explain`]({{ src }}/kit/explain) derives the human form at read time by
replaying the chain: label trails, keyed-element identity, id→name display
values, bookkeeping flags (a bare field name matched at any depth, or an
index-free schema path matching its field and subtree), and per-field
`ValueNode` breakdowns of container values. The caller's schema arrives as `DiffOption`s — the same vocabulary
`Diff` uses on the write side — so changing labels, name fields, or ignored
fields re-renders *all* existing history without touching a stored byte.

One opt-in exception, stored *outside* the seal: `chroniclekit.WithReadable()`
freezes each commit's display rows — including id→name pairs the caller passes
as `WithNames` at seal time — into a per-commit sidecar via the backend's
optional `Annotator` capability. It exists because read-time resolution cannot
recover the name of a referent deleted or renamed since the change was
recorded. The sealed record stays the authority (the sidecar never enters the
hash preimage and deleting it is always safe); commits without one — all
pre-feature history — keep decorating live.

The boundary the API holds: the kit emits structure (slices, names, canonical
scalars, flags), never formatting —
[why that line is drawn there]({{ u_design }}#what-is-deliberately-absent).

## Cursor (keyset) pagination

`TailReader.CommitsAfter(docID, afterID, limit)` — "the commits after this
one" — rather than OFFSET-style paging. Keyset cursors stay O(page) at any
depth and are the primitive under both the snapshot fast path and incremental
verification. Adapters implement it with an indexed range scan
([`adapters/sql/capability.go`]({{ src }}/adapters/sql/capability.go)).

## Capability pattern (graceful degradation)

Backends implement optional interfaces — `Indexer`, `TailReader`,
`Snapshotter`, `Deduper` ([`core/capability.go`]({{ src }}/core/capability.go)) —
discovered by Go type assertion. A backend without a capability, or a stale
or corrupt cache, degrades to the slower correct path (full replay), never to
a wrong answer. This is the Go-idiomatic strategy pattern: behavior selected
at runtime by what the value can do, not by configuration.

## Fork-tolerant append (forked lineage)

Writers append; the Log never rejects on concurrency grounds. A commit's
parent is the snapshot the writer built against — `RecordPatch` anchors it to
the head its state read reached (`StateWithHead`), and `WithParent` lets any
caller assert it ([`core/recorder.go`]({{ src }}/core/recorder.go)). Two
writers racing the same document therefore record two commits sharing a
parent: a **forked lineage** — two honest facts, both kept, folded
last-write-wins in arrival (seq) order at read time. Learned from git, not
copied: the anchoring that keeps each commit truthful, without branches, refs,
or merge machinery.

The read side is a small **eventual consistency** model: a fork means two
writers held different views for a moment, and the fold reconciles them —
every reader replaying the same arrival order converges on the same state,
deterministically. What is *never* eventual is the record itself: both sides
of the fork are durable the instant they append.

**Arrival order** means the order the backend durably accepted the appends —
the in-memory adapter's slice order, SQL's per-document `seq`, ClickHouse's
`at` timestamp, where producer clock skew can reorder it (the tradeoffs are on
[operations]({{ '/documentation/operations/' | relative_url }})). And the fold
never edits the record: a change's `From` values were diffed against the state
*its* writer read, so on the side the fold overrides they can differ from the
folded result — honest provenance of that writer's view, not a replay
precondition. Replay applies `To` values only.

The layering is deliberate. The atomic append primitive is the adapter's job;
declaring which snapshot a commit was built against can only ever be the
writer's. OCC and ACID — retries, conflict errors, expected-version checks —
belong to the persistence layer or the producer (a store's compare-and-swap, a
single writer per document). The library records what happened; it does not
referee it.

## Idempotency keys (idempotent consumer)

The **idempotent consumer** pattern: at-least-once delivery (queues, webhooks,
retrying HTTP clients) is made exactly-once at the log. `Seal` with
`WithIdempotencyKey` consults the `Deduper` and returns the original commit
for a replayed key instead of sealing a duplicate — the producer retries
freely, the log absorbs the duplicates. Keys are scoped per document.

## Lowest common ancestor (LCA)

`CommitSnapshot` scopes its output to the deepest path prefix shared by all
of a commit's changed paths (`lcaPath`, [`kit/view`]({{ src }}/kit/view)) — the
smallest subtree that contains the whole diff. Segment-wise longest common
prefix, climbing to the nearest enclosing container when the LCA is a scalar.

## Prior art

Systems and paradigms chronicle borrows from — useful when explaining what it
is, or looking up how a problem was solved elsewhere. Ranked by closeness.

- **git** — the write model: stage, seal into a content-addressed commit,
  hash-chain to the parent. See [the git model]({{ u_model }}) for the
  call-by-call side-by-side, the stored-diffs-not-snapshots inversion, and the
  one deliberate divergence (commit metadata stays out of the hash).
- **Event sourcing / CQRS** (Greg Young; Kafka's log-as-truth) — the
  architecture: the append-only change log is the source of truth, every state
  is a derived projection.
- **Temporal databases** — the read model. Datomic's `as-of` query is exactly
  `StateAt`; XTDB and SQL:2011 temporal tables (`FOR SYSTEM_TIME AS OF`) are
  the same idea. chronicle tracks commit time only (no separate valid-time
  axis, i.e. not fully bitemporal).
- **Verifiable logs** — the integrity model. Certificate Transparency
  (RFC 6962) runs public tamper-evident append-only logs with incremental
  consistency proofs; `VerifyAfter` is the linear-chain version of that idea.
  AWS QLDB, immudb, and Google Trillian are ledger databases in the same
  family. A blockchain minus consensus: one writer, no proof-of-anything.
- **Audit-trail libraries** (Rails PaperTrail, Hibernate Envers, Django
  simple-history) — the product niche: per-record change history with
  who/when/what. chronicle is that niche made storage-agnostic, diff-native,
  and tamper-evident.
- **Write-ahead log (WAL)** — the database-internal ancestor: append-only log
  as truth, tables as projections.
- **Change data capture** (Debezium) — adjacent but inverted: CDC extracts
  changes from a database after the fact; chronicle records them first-class
  at write time.
- **CRDTs / operational transformation** — the deliberate contrast, and a
  natural companion. Those *resolve* concurrent divergent histories; chronicle
  only *records* them (a fork of sibling commits, folded last-write-wins at
  read) and will not grow merge semantics. But because a commit stores an
  *ordered list of operations* rather than a snapshot, a CRDT application can
  hand its settled order straight to `RecordChanges`: replay converges
  last-write-wins, and each actor's operation survives the merge that
  collapsed it. See
  [where changes come from]({{ '/documentation/kit/#where-changes-come-from' | relative_url }}).
