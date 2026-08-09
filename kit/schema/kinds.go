// Package chronicleschema is the vocabulary a caller uses to declare its
// document's shape to the kit: array identity, value types, labels,
// bookkeeping fields, display names. One Config, two consumers —
// chroniclediff.Diff shapes writes with it and chronicleexplain.Explain
// decorates reads with it.
//
// It holds only what a caller sets. The path grammar, JSON canonicalization,
// and change replay the kit's packages share with each other live in
// kit/internal/docmodel, out of a caller's way. This package depends on
// chronicle/core plus the standard library; core's Change.Path stays an opaque
// string, and this grammar lives only in the kit.
package chronicleschema

import "github.com/zdirnecamlcs96/chronicle/kit/internal/docmodel"

// Kinds the kit emits. Free-form in core; the kit fixes this small vocabulary
// so Diff output and replay agree.
const (
	KindCreate = docmodel.KindCreate // a path that did not exist before
	KindPut    = docmodel.KindPut    // a leaf value changed
	KindDelete = docmodel.KindDelete // a path that no longer exists
)
