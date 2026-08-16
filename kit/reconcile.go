package chroniclekit

import (
	"context"
	"errors"
	"sort"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
	chroniclediff "github.com/zdirnecamlcs96/chronicle/kit/diff"
	"github.com/zdirnecamlcs96/chronicle/kit/internal/docmodel"
	chronicleschema "github.com/zdirnecamlcs96/chronicle/kit/schema"
)

// ErrNoIndexer is returned by ReconcileSweep when the Kit's backend exposes no
// changelog.Indexer — enumerating every document in the log (rather than one
// named by the caller) is exactly what that capability answers.
var ErrNoIndexer = errors.New("chroniclekit: reconcile sweep requires a Log with Indexer")

// Drift is the field-level difference between a document's log-replayed state
// and its live database row, as produced by Reconcile. It reuses
// chroniclediff's vocabulary rather than a second differ: each entry's From is
// what the log says, To is what the database currently holds.
//
// Drift carries NO verdict — only the operator can weigh intent. A delete
// entry (the log has the field, the row lacks it) leans toward "commit sealed
// but the database write failed" — a phantom record. A create entry (the row
// has the field, the log never recorded it) leans toward "a write bypassed
// chronicle entirely". A put entry is the ambiguous case: a legitimate
// concurrent edit and an out-of-band overwrite look identical once the log's
// before-value is gone, so Reconcile reports what differs and leaves the read
// to a human.
type Drift struct {
	Changes []changelog.Change
}

// Empty reports whether the replayed state and the live row agree on every
// (non-ignored) field.
func (d Drift) Empty() bool { return len(d.Changes) == 0 }

// Reconcile compares docID's state replayed from the log against actual — the
// caller's live database row, projected into the same document shape a writer
// would pass to RecordUpdate (the same projection contract: chronicle never
// guesses at the caller's schema). It proves the log is complete and accurate,
// which the hash chain does not: VerifyChain proves the log was not edited,
// not that it recorded everything that happened.
//
// opts declares the caller's schema exactly as WithDiffOptions does for a
// write. A WithIgnoredFields entry is excluded from the result outright here —
// inverted from Explain, where it is display-only and the field is still
// recorded — because a bookkeeping column like updated_at would otherwise
// drift on every single call.
//
// Read consistency is the caller's responsibility: run against a row read in
// the same transaction/snapshot as the log read, or treat a non-empty Drift as
// advisory — an in-flight legitimate write looks identical to drift until it
// lands.
func (k *Kit) Reconcile(ctx context.Context, docID string, actual any, opts ...chronicleschema.Option) (Drift, error) {
	replayed, err := k.State(ctx, docID)
	if err != nil {
		return Drift{}, err
	}
	changes, err := chroniclediff.Diff(replayed, actual, opts...)
	if err != nil {
		return Drift{}, err
	}
	cfg := chronicleschema.New(opts...)
	kept := make([]changelog.Change, 0, len(changes))
	for _, ch := range changes {
		if !bookkeeping(&cfg, replayed, ch.Path) {
			kept = append(kept, ch)
		}
	}
	return Drift{Changes: kept}, nil
}

// bookkeeping reports whether path touches a WithIgnoredFields entry — its
// bare name at any depth, or its declared schema path and subtree — walking
// replayed (the diff's before-state) via docmodel.ClassifySegment to classify
// each segment as an array index or an object key, the same classifier
// chronicleexplain's decorate uses.
func bookkeeping(cfg *chronicleschema.Config, replayed any, path string) bool {
	var schema []string
	cur := replayed
	for _, seg := range docmodel.SplitPath(path) {
		isIndex, _, elem := docmodel.ClassifySegment(cur, seg)
		if isIndex {
			cur = elem
			continue
		}
		schema = append(schema, seg)
		if cfg.Bookkeeping(seg, schema) {
			return true
		}
		cur = elem
	}
	return false
}

// SweepReport is ReconcileSweep's result: the two ways a document's log side
// and database side can disagree about EXISTENCE. Per-field drift on a
// document both sides agree exists is Reconcile's job, not Sweep's.
type SweepReport struct {
	// LogOnly holds document ids the log has committed history for that are
	// absent from dbDocIDs — a deleted row, or one that never materialized
	// after a commit landed.
	LogOnly []string
	// DBOnly holds ids in dbDocIDs the log has no commits for — a row written
	// without ever going through chronicle.
	DBOnly []string
}

// ReconcileSweep is the deletion half of drift detection: Reconcile only ever
// runs against a document someone names, so it never notices one that
// vanished entirely. Sweep and the core inventory checkpoint
// (ComputeCheckpoint/VerifyCheckpoint) cover opposite halves of the same
// concern — the checkpoint catches log-side tampering (a commit, or a whole
// document, quietly removed from the log itself); Sweep catches database-side
// disappearance (a row deleted, or never written, on the live side the log
// cannot see). Sweep reports existence only; follow up with Reconcile per
// flagged document for the field-level detail.
//
// dbDocIDs is the caller's live document id set — same projection contract as
// Reconcile: chronicle never queries the caller's database. Enumerate it at
// the same snapshot as the log read, or treat the result as advisory — a
// document mid-write can transiently appear on only one side.
func (k *Kit) ReconcileSweep(ctx context.Context, dbDocIDs []string) (SweepReport, error) {
	idx := indexerFor(k.svc)
	if idx == nil {
		return SweepReport{}, ErrNoIndexer
	}
	all, err := idx.AllCommits(ctx, 0)
	if err != nil {
		return SweepReport{}, err
	}
	logDocs := make(map[string]struct{}, len(all))
	for _, dc := range all {
		logDocs[dc.DocID] = struct{}{}
	}
	dbDocs := make(map[string]struct{}, len(dbDocIDs))
	for _, id := range dbDocIDs {
		dbDocs[id] = struct{}{}
	}

	var report SweepReport
	for id := range logDocs {
		if _, ok := dbDocs[id]; !ok {
			report.LogOnly = append(report.LogOnly, id)
		}
	}
	for id := range dbDocs {
		if _, ok := logDocs[id]; !ok {
			report.DBOnly = append(report.DBOnly, id)
		}
	}
	sort.Strings(report.LogOnly)
	sort.Strings(report.DBOnly)
	return report, nil
}

// indexerFor walks svc's Unwrap() chain — the same walk annotatorFor and
// chronicleview.New use for their own optional capabilities — looking for a
// Log that exposes changelog.Indexer. nil means none does.
//
// Service.AllCommits already delegates to Indexer, but silently returns an
// empty result on a backend without one (core/service.go) — indistinguishable
// from an empty log. ReconcileSweep needs to error instead, the same way
// core/verify.go's VerifyAfter gates on TailReader, so this type-asserts the
// Log directly rather than going through Service.AllCommits.
func indexerFor(svc changelog.Service) changelog.Indexer {
	var l changelog.Log
	if u, ok := svc.(interface{ Unwrap() changelog.Log }); ok {
		l = u.Unwrap()
	}
	for l != nil {
		if idx, ok := l.(changelog.Indexer); ok {
			return idx
		}
		u, ok := l.(interface{ Unwrap() changelog.Log })
		if !ok {
			break
		}
		l = u.Unwrap()
	}
	return nil
}
