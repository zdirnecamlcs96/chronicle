package changelog

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"hash"
	"sort"
	"time"
)

// Commit is a git-style commit: an immutable, content-addressed bundle of Changes
// hash-chained to its parent.
//
// Deliberately UNLIKE git, the hash (ID) covers only (Parent, Message, Changes) —
// NOT At or Authors. So the same logical change sealed by different producers, or
// at different times, yields the SAME ID. That content-addressing is what lets a
// retried delivery dedup to a single commit (see Deduper).
//
//   - ID       the commit hash (see computeID).
//   - Parent   the previous commit's ID; "" marks the root (first commit on a doc).
//   - At       when it was sealed (NOT hashed).
//   - Authors  the distinct Change actors, sorted (NOT hashed).
//   - Message  an optional annotation from WithMessage; IS hashed, so editing it
//              after the fact breaks the chain.
//   - Changes  the edits this commit seals — its diff.
type Commit struct {
	ID      string    `json:"id"`
	Parent  string    `json:"parent"`
	At      time.Time `json:"at"`
	Authors []string  `json:"authors"`
	Message string    `json:"message,omitempty"`
	Changes []Change  `json:"changes"`
}

// computeID is the commit's content address: SHA-256 over the parent ID, the
// message, and the canonical JSON of the changes — deliberately NOT the time or
// authors. Equal (parent, message, changes) always yield the same ID; any
// difference changes it — a tamper-evident chain. Each field is length-framed
// (see writeField) so the boundaries between parent, message, and changes are
// unambiguous; without that framing a root commit (parent="") whose message
// began with a real commit id would hash the same bytes as the child commit
// holding that id as its parent, forging a collision.
func computeID(parent, message string, changes []Change) (string, error) {
	payload, err := json.Marshal(changes)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	writeField(h, []byte(parent))
	writeField(h, []byte(message))
	writeField(h, payload)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// writeField hashes b prefixed by its length as 8 big-endian bytes, framing the
// field so adjacent fields cannot be re-split into a colliding preimage.
func writeField(h hash.Hash, b []byte) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(b)))
	h.Write(n[:])
	h.Write(b)
}

// distinctAuthors returns the sorted, de-duplicated actor list of the changes.
func distinctAuthors(changes []Change) []string {
	seen := map[string]struct{}{}
	for _, c := range changes {
		seen[c.Actor] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for a := range seen {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}
