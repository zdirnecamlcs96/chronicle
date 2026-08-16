package changelog

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
)

// sigPreimageV1 is the domain-separation tag written as the first, unframed
// bytes of the commit-signature preimage — see commitPreimageV1 in commit.go
// for the tag convention. The signed message is sigPreimageV1 followed by the
// commit's ID (its hex hash string, as bytes); the domain tag stops a
// signature made for some other chronicle.*.v1 preimage, or an unrelated
// protocol entirely, from being replayed here.
const sigPreimageV1 = "chronicle.sig.v1\n"

// sigMessage builds the message signed and verified for commit id: the
// domain tag followed by id's bytes.
func sigMessage(id string) []byte {
	return append([]byte(sigPreimageV1), []byte(id)...)
}

// signCommit signs id with signer and returns the raw Ed25519 signature.
// signer's public key must be an ed25519.PublicKey — WithSigner accepts any
// crypto.Signer (so a KMS/HSM-backed signer works), but chronicle only
// speaks Ed25519, so a different key type is rejected here, at the first
// commit sealed under it.
func signCommit(signer crypto.Signer, id string) ([]byte, error) {
	if _, ok := signer.Public().(ed25519.PublicKey); !ok {
		return nil, fmt.Errorf("changelog: signer public key is %T, want ed25519.PublicKey", signer.Public())
	}
	return signer.Sign(rand.Reader, sigMessage(id), crypto.Hash(0))
}

// KeyResolver looks up the Ed25519 public key registered under keyID (a
// Commit's SigKeyID); ok is false if the caller's key store has no such key.
// VerifySignatures calls it once per signed commit.
type KeyResolver func(keyID string) (ed25519.PublicKey, bool)

// SignatureReport is VerifySignatures' result: every commit in the fetched
// history classified into exactly one of Unsigned/Valid/Invalid/UnknownKey,
// with counts and the offending commit ids for the two failure classes.
// Report-don't-verdict: whether a document's commits must all be signed is
// caller policy, so an unsigned or invalid commit is never an error return.
type SignatureReport struct {
	Total      int
	Unsigned   int
	Valid      int
	Invalid    int
	UnknownKey int

	// InvalidIDs holds the ids of commits whose Signature did not verify
	// under its resolved SigKeyID.
	InvalidIDs []string
	// UnknownKeyIDs holds the ids of signed commits whose SigKeyID did not
	// resolve.
	UnknownKeyIDs []string
}

// VerifySignatures fetches docID's full history from log and classifies each
// commit's signature:
//   - Unsigned    Signature is empty — never signed.
//   - Valid       SigKeyID resolves and the signature verifies under that key.
//   - Invalid     SigKeyID resolves but the signature does not verify —
//     either the bytes were tampered, or SigKeyID was retargeted to a
//     different (but still registered) key than the one that actually
//     signed.
//   - UnknownKey  SigKeyID does not resolve — resolve returned ok=false,
//     including a SigKeyID retargeted to a label no key is registered under.
//
// The error return is reserved for operational failures (log/context
// errors), mirroring Verify's result/error split — a bad or unresolvable
// signature is a report finding, never an error.
func VerifySignatures(ctx context.Context, log Log, docID string, resolve KeyResolver) (SignatureReport, error) {
	commits, err := log.Commits(ctx, docID, 0)
	if err != nil {
		return SignatureReport{}, err
	}
	var report SignatureReport
	report.Total = len(commits)
	for _, c := range commits {
		if len(c.Signature) == 0 {
			report.Unsigned++
			continue
		}
		pub, ok := resolve(c.SigKeyID)
		if !ok {
			report.UnknownKey++
			report.UnknownKeyIDs = append(report.UnknownKeyIDs, c.ID)
			continue
		}
		if ed25519.Verify(pub, sigMessage(c.ID), c.Signature) {
			report.Valid++
		} else {
			report.Invalid++
			report.InvalidIDs = append(report.InvalidIDs, c.ID)
		}
	}
	return report, nil
}
