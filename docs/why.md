---
title: Why chronicle exists
permalink: /documentation/why/
eyebrow: why
source: why.md
summary: >-
  Every incident — a system bug or a user disputing what they see — turns into
  the same question about the data: what was it, what is it now, when did it
  change, and who changed it. Current state cannot answer that. chronicle
  records the answer where the data lives, behind a port so it can live
  wherever you already operate.
---
{%- assign src = site.repo | append: '/blob/main' -%}
{%- assign u_reference = '/documentation/reference/' | relative_url -%}
{%- assign u_architecture = '/documentation/architecture/' | relative_url -%}
{%- assign u_concepts = '/documentation/concepts/' | relative_url -%}
{%- assign u_operations = '/documentation/operations/' | relative_url -%}
{%- assign u_getting_started = '/documentation/getting-started/' | relative_url -%}

## The question every investigation starts with

Something is wrong. Maybe an order shipped at the wrong price, maybe a customer
insists they never changed their delivery address. Before anyone can say whether
it is a bug in the code or a misunderstanding at the interface, they have to
establish the same four facts about the data:

- what was the value before,
- what is it now,
- when did it change,
- and who changed it.

Until those are settled, every theory is speculation. Answering them is the
first move in an investigation, not a detail you chase afterwards.

## Current state cannot answer it

A row in a database holds exactly one thing: what the value is now. That is the
one fact nobody is arguing about.

The usual substitutes each fall short in a specific way:

- **`updated_at`** proves *something* changed — not which field, from what, or
  by whom.
- **Status columns and soft deletes** capture the two or three transitions
  someone modelled in advance. The field that caused the incident is rarely one
  of them.
- **Application logs** describe *requests*, not *data*. Sampled, lossy under
  load, usually kept thirty days, and a request that succeeded often logs
  nothing about what it wrote.
- **Binlogs and CDC streams** do record every write, but at row granularity,
  without the actor, and with a retention window measured in days. They are a
  replication mechanism that happens to be readable.

Each is a partial record kept for a different purpose. None was designed to be
asked "what happened to this document."

## Bugs and UX complaints need the same evidence

The two failure classes look unrelated and resolve identically.

For a **system bug**, the question is whether the code wrote the wrong value or
whether it was already wrong on arrival — a before/after pair at a specific
write, not the current state.

For a **UX issue**, the user's account and the system's behaviour disagree, and
the resolution is who touched the field and when. Session replays have expired
and support tickets are hearsay.

Both come down to the data's own history. Traces, metrics, and logs corroborate
once you know what changed; they are a poor place to start, because they
describe the machinery rather than the thing being argued about.

## What chronicle records

Every edit is a `Change`: `Path` (which field), `Kind` (create, put, delete),
`From` and `To` (the values either side), `Actor` (who), `At` (when). Changes
are staged, then sealed into a `Commit` whose ID is
`SHA-256(parent ID, message, changes)`. Because the parent's ID is inside the
hash, altering any historical commit changes every ID after it.

That last property is the point. An audit table anyone can `UPDATE` is not
evidence; it is another mutable table that happens to be named "history". A
hash chain makes tampering detectable, so the record can be trusted in exactly
the situation it exists for — the one where somebody's account of events is
being questioned. The mechanics are in
[concepts]({{ u_concepts }}); the per-call contract is in
[reference]({{ u_reference }}).

The scope is deliberately narrow: **changes to persisted data**. Not requests,
not user interactions, not application events. Those belong in systems built
for them, and they answer different questions.

## Why it is storage-agnostic

A changelog is only useful if it outlives the incident, which means it has to
live somewhere the team already runs, backs up, and pays for. A library that
picked a database for you would make adoption conditional on adopting that
database — and the teams who most need change tracking are the ones with the
most established infrastructure.

So the core depends on nothing but the standard library, and talks to storage
through a port of three methods:

```go
AppendCommit(ctx context.Context, docID string, c Commit) error
Commits(ctx context.Context, docID string, limit int) ([]Commit, error)
Head(ctx context.Context, docID string) (string, error)
```

Anything that can satisfy those three is a backend. The core never imports an
adapter; see [architecture]({{ u_architecture }}) for how that separation is
enforced by module boundaries rather than convention.

## Why adapters are a layer, not a list

The shipped adapters — in-memory for dev, MySQL for transactional writes,
ClickHouse for columnar retention — are conveniences. They are not the contract.

Retention windows, compliance requirements, cost ceilings, and existing
infrastructure differ enough that someone will always need a backend that is not
on the list: Postgres, DynamoDB, object storage, or an internal service that
already owns durability. If those users were second-class, the storage-agnostic
claim would be marketing.

What makes a custom backend first-class is
[`core/conformance`]({{ src }}/core/conformance) — an executable definition of
what the port guarantees. Implement `Log`, run `RunLogConformance` against it,
and it is as correct as anything shipped here, because "correct" is a test suite
rather than a maintainer's opinion. The optional capabilities
(`Indexer`, `Deduper`, `TailReader`, `Snapshotter`) work the same way: implement
one, prove it with its conformance run, and callers pick it up automatically.
Skip it, and everything still works.

## What this is not

- Not an APM or tracing system. It records data transitions, not spans.
- Not application logging. Nothing here replaces the logs you already emit.
- Not an event bus. Commits are a durable record to read back, not a delivery
  mechanism.
- Not an access-control or authentication layer. `Change.Actor` is whatever you
  pass; the library trusts your caller and never establishes identity itself.

The full list of deliberate omissions — no merges, no retention policy, no auth
anywhere — is in [reference]({{ u_reference }}). What running it costs is in
[operations]({{ u_operations }}).

Ready to try it? [Getting started]({{ u_getting_started }}) goes from `go get` to
a verified, durable changelog in eight steps.
