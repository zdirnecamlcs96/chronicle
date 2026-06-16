package changelog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
//              after the fact breaks the chain. "" hashes to zero bytes, leaving
//              pre-Message commit IDs untouched.
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
// difference changes it — a tamper-evident chain. An empty message writes zero
// bytes, so a commit made without one hashes identically to a pre-Message commit.
func computeID(parent, message string, changes []Change) (string, error) {
	payload, err := json.Marshal(changes)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write([]byte(parent))
	h.Write([]byte(message))
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil)), nil
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
