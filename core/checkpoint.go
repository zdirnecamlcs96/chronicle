package changelog

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"sort"
	"time"
)

// DocState is one document's summary in a Checkpoint's inventory: enough to
// detect deletion and truncation without storing the whole history.
//   - Heads    the document's tip commit ids (see Tipper) — a truncated or
//     rewritten tail drops one of these from the document's current commit
//     ids.
//   - Commits  the total commit count — catches interior deletion, which
//     leaves Heads unchanged.
type DocState struct {
	DocID   string
	Heads   []string // tip commit ids
	Commits int      // total commits in the doc's chain
}

// Checkpoint is a point-in-time inventory of every document's tips and commit
// counts, digested into one hash a consumer anchors externally (see
// Anchorer). It closes the gap Verify/VerifyAfter cannot: both operate
// per-document and so cannot notice a document whose entire history is gone,
// or a tail truncated behind a stale anchor.
type Checkpoint struct {
	At        time.Time  // informational, NOT hashed (same precedent as Commit.At)
	Inventory []DocState // sorted by DocID; each DocState.Heads sorted
	Digest    string     // hex SHA-256 over the canonical preimage — see checkpointDigest
}

// checkpointPreimageV1 is the domain-separation tag written as the first,
// unframed bytes of the checkpoint preimage — see commitPreimageV1 for the
// convention. Any change to the encoding bumps the version.
const checkpointPreimageV1 = "chronicle.checkpoint.v1\n"

// checkpointDigest is the checkpoint's content address: SHA-256 over a fixed
// version tag, then inv in canonical form — entries sorted by DocID; per
// entry, the length-framed DocID, then an 8-byte big-endian head COUNT, then
// each head length-framed with Heads sorted lexicographically (canonical: the
// order a backend happens to return them in is not trusted), then Commits as
// 8 big-endian bytes. The head count makes each entry self-delimiting: without
// it, a raw Commits value is byte-indistinguishable from a following head's
// own length prefix, letting two different inventories hash the same way
// (e.g. one head whose framed bytes happen to equal a Commits field glued to
// the next entry's framed DocID). No entry count is needed at the top level —
// each entry already starts with a length-framed DocID, so entries remain
// unambiguous when simply concatenated to end of input, the same way a
// framed-field sequence needs no outer count once every field's own boundary
// is explicit. Sorting both levels makes the digest independent of inv's
// input order. At is excluded (see Checkpoint.At).
func checkpointDigest(inv []DocState) string {
	sorted := append([]DocState(nil), inv...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].DocID < sorted[j].DocID })

	h := sha256.New()
	h.Write([]byte(checkpointPreimageV1))
	for _, ds := range sorted {
		writeField(h, []byte(ds.DocID))
		heads := append([]string(nil), ds.Heads...)
		sort.Strings(heads)
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(heads)))
		h.Write(n[:])
		for _, head := range heads {
			writeField(h, []byte(head))
		}
		binary.BigEndian.PutUint64(n[:], uint64(ds.Commits))
		h.Write(n[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// docAcc is the per-document scan accumulator behind ComputeCheckpoint and
// VerifyCheckpoint: the full commit id set (needed to check whether a
// recorded head still exists anywhere in the current chain, not just among
// current heads — a head that gained a child is legitimate growth, not
// truncation), the set of ids referenced as a parent (to derive heads), and
// the count.
type docAcc struct {
	ids     map[string]struct{}
	parents map[string]struct{}
	count   int
}

// scanInventory scans all documents' commits (any order — AllCommits' order
// is irrelevant to the result) into a per-document accumulator.
func scanInventory(all []DocCommit) map[string]*docAcc {
	docs := map[string]*docAcc{}
	for _, dc := range all {
		a, ok := docs[dc.DocID]
		if !ok {
			a = &docAcc{ids: map[string]struct{}{}, parents: map[string]struct{}{}}
			docs[dc.DocID] = a
		}
		a.ids[dc.Commit.ID] = struct{}{}
		if dc.Commit.Parent != "" {
			a.parents[dc.Commit.Parent] = struct{}{}
		}
		a.count++
	}
	return docs
}

// docState derives docID's public DocState (sorted Heads = ids minus
// parents) from the accumulator.
func (a *docAcc) docState(docID string) DocState {
	heads := make([]string, 0, len(a.ids))
	for id := range a.ids {
		if _, isParent := a.parents[id]; !isParent {
			heads = append(heads, id)
		}
	}
	sort.Strings(heads)
	return DocState{DocID: docID, Heads: heads, Commits: a.count}
}

// ComputeCheckpoint builds a fresh Checkpoint from log's current state: one
// pass over every document via the Indexer capability, computing each
// document's Heads and Commits, then digesting the result. Requires a Log
// with Indexer. Cost is O(entire log) in memory — see the AllCommits scan
// below; schedule checkpointing accordingly on large logs.
func ComputeCheckpoint(ctx context.Context, log Log) (Checkpoint, error) {
	idx, ok := log.(Indexer)
	if !ok {
		return Checkpoint{}, errors.New("changelog: checkpoint requires a Log with Indexer")
	}
	// ponytail: AllCommits(ctx, 0) materializes every commit (full Changes
	// payloads) for every document at once — O(entire log) memory. Fine for
	// the logs this ships against; once a log outgrows memory, replace with a
	// streaming/paginated read or a per-doc Tipper+count path that avoids
	// loading Changes at all.
	all, err := idx.AllCommits(ctx, 0)
	if err != nil {
		return Checkpoint{}, err
	}
	docs := scanInventory(all)
	docIDs := make([]string, 0, len(docs))
	for id := range docs {
		docIDs = append(docIDs, id)
	}
	sort.Strings(docIDs)
	inv := make([]DocState, 0, len(docIDs))
	for _, id := range docIDs {
		inv = append(inv, docs[id].docState(id))
	}
	return Checkpoint{At: time.Now().UTC(), Inventory: inv, Digest: checkpointDigest(inv)}, nil
}

// ShrunkDoc records one document whose current commit count is lower than
// what a checkpoint recorded — interior deletion, which leaves Heads
// unaffected and so would otherwise pass a heads-only check.
type ShrunkDoc struct {
	DocID    string
	Recorded int
	Current  int
}

// CheckpointReport is VerifyCheckpoint's result: every finding, not just the
// first one hit.
type CheckpointReport struct {
	OK bool
	// Doctored is true when cp.Digest does not match the recomputed digest of
	// cp.Inventory — the checkpoint itself was edited after the fact. Its
	// Inventory can then not be trusted, so VerifyCheckpoint returns without
	// comparing against the log at all; the fields below are left unset.
	Doctored bool
	// MissingDocs holds checkpointed document ids with no commits at all
	// now — their entire history was deleted.
	MissingDocs []string
	// MissingHeads holds, per document still present, the recorded head ids no
	// longer among its current commit ids — the tail was truncated or
	// rewritten past that head.
	MissingHeads map[string][]string
	// ShrunkDocs holds, per document still present, its recorded vs. current
	// commit counts when current < recorded.
	ShrunkDocs []ShrunkDoc
}

// VerifyCheckpoint checks cp against log's current state. Chains legitimately
// GROW after a checkpoint, so this is not an equality check: documents new
// since cp are ignored, and additional commits on a checkpointed document are
// expected and do not fail it. Requires a Log with Indexer, same as
// ComputeCheckpoint. The error return is reserved for operational failures
// (context, storage) — tamper findings are reported in CheckpointReport,
// never as an error, mirroring the result/error split Verify uses.
func VerifyCheckpoint(ctx context.Context, log Log, cp Checkpoint) (CheckpointReport, error) {
	if checkpointDigest(cp.Inventory) != cp.Digest {
		return CheckpointReport{Doctored: true}, nil
	}
	idx, ok := log.(Indexer)
	if !ok {
		return CheckpointReport{}, errors.New("changelog: checkpoint verification requires a Log with Indexer")
	}
	// ponytail: same O(entire log) memory ceiling as ComputeCheckpoint's scan — see its comment.
	all, err := idx.AllCommits(ctx, 0)
	if err != nil {
		return CheckpointReport{}, err
	}
	docs := scanInventory(all)

	report := CheckpointReport{OK: true}
	for _, recorded := range cp.Inventory {
		cur, ok := docs[recorded.DocID]
		if !ok {
			report.OK = false
			report.MissingDocs = append(report.MissingDocs, recorded.DocID)
			continue
		}
		var missing []string
		for _, head := range recorded.Heads {
			if _, ok := cur.ids[head]; !ok {
				missing = append(missing, head)
			}
		}
		if len(missing) > 0 {
			report.OK = false
			if report.MissingHeads == nil {
				report.MissingHeads = map[string][]string{}
			}
			report.MissingHeads[recorded.DocID] = missing
		}
		if cur.count < recorded.Commits {
			report.OK = false
			report.ShrunkDocs = append(report.ShrunkDocs, ShrunkDoc{
				DocID: recorded.DocID, Recorded: recorded.Commits, Current: cur.count,
			})
		}
	}
	return report, nil
}

// Anchorer is implemented by the CONSUMER of chronicle, not a storage
// backend — unlike Indexer/TailReader/Snapshotter/Annotator/Deduper/Tipper in
// capability.go, all of which a Log adapter implements. An anchor stored in
// the same database it guards is decoration: a hostile database owner who can
// edit the log can edit an anchor sitting beside it just as easily. Anchor
// must persist the checkpoint somewhere that owner cannot reach — another
// organization's store, WORM storage, even a printout — the same trust
// contract docs/concepts.md documents for VerifyAfter's anchor. Core ships no
// implementation; wire whichever external store fits the deployment.
type Anchorer interface {
	// Anchor persists cp externally, outside the database its Inventory
	// describes.
	Anchor(ctx context.Context, cp Checkpoint) error
	// LatestAnchor returns the most recently anchored Checkpoint; ok is false
	// if none has been anchored yet.
	LatestAnchor(ctx context.Context) (Checkpoint, bool, error)
}
