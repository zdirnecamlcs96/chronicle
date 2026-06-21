# Design: parent snapshot — SUPERSEDED

**Date:** 2026-06-21
**Status:** SUPERSEDED by [chroniclekit](2026-06-21-chroniclekit-design.md). Do not implement this document.

## Why this exists / what changed

This spec originally proposed an **optional per-`Change` `Snapshot` string** in
`core`: each change carries the enclosing parent object of its path, producer-
supplied, opaque, excluded from the commit hash. It was implemented and then
**fully reverted** (the code never reached a commit) after the design converged
somewhere better.

The decision evolved across three steps:

1. **Per-change parent snapshot in core** (this doc, original). Rejected: it
   stored the same parent repeatedly when several changes shared one parent, and
   it pushed document/grammar concerns toward `core`, which is deliberately
   source-agnostic (no document, no path grammar).
2. **Deduped immediate-parents, then per-commit LCA.** The requirement settled
   on **one snapshot per commit**, scoped to the **lowest common ancestor (LCA)**
   of all the commit's changed paths — the longest common path prefix, computed
   segment-wise (NOT LCS, which is a sequence-diff algorithm). Because a commit's
   changes are always within one document (a `Recorder` is bound to one `docID`),
   a common ancestor always exists; when changes scatter, the LCA collapses to
   the document root (whole-document snapshot) — an accepted trade-off.
3. **Derive on read, in the kit — not stored in core.** The LCA snapshot is
   computed by `chroniclekit` at read/render time from the replayed document
   state, where both a document and a path grammar exist. `core` and all adapters
   stay **unchanged** (no `Snapshot` field, no new column, no migration). For a
   complete log this is identical to a stored snapshot, and free.

## Net effect on core

**None.** `core` keeps its original `Change`/`Commit` shapes and `computeID`. The
snapshot capability lives entirely in `chroniclekit` (`view.go`). See the
[chroniclekit design](2026-06-21-chroniclekit-design.md) for the real spec.
