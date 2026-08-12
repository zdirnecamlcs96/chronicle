package chroniclekit

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"slices"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
	chronicleexplain "github.com/zdirnecamlcs96/chronicle/kit/explain"
	chronicleschema "github.com/zdirnecamlcs96/chronicle/kit/schema"
)

// saveReadable computes and stores the just-sealed commit's readable sidecar.
// Best-effort, NEVER failing the commit: a compute failure stores an error
// stub instead of rows; a persist failure retries with the stub so readers can
// still see and acknowledge the gap; and when even that fails, it logs and
// moves on — the sidecar is simply absent.
func (k *Kit) saveReadable(ctx context.Context, docID string, c changelog.Commit, cfg *recordConfig) {
	var payload chronicleexplain.Readable
	rows, err := chronicleexplain.ExplainChanges(cfg.annotateBase, c.Changes, cfg.diffOpts...)
	if err != nil {
		payload = chronicleexplain.Readable{Error: err.Error()}
	} else {
		payload = chronicleexplain.ReadableOf(rows)
	}
	if err := k.putReadable(ctx, docID, c.ID, payload); err != nil {
		if err2 := k.putReadable(ctx, docID, c.ID, chronicleexplain.Readable{Error: err.Error()}); err2 != nil {
			log.Printf("chroniclekit: readable sidecar %s/%s dropped: %v", docID, c.ID, err2)
		}
	}
}

func (k *Kit) putReadable(ctx context.Context, docID, commitID string, r chronicleexplain.Readable) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return k.ann.SaveAnnotation(ctx, changelog.Annotation{DocID: docID, CommitID: commitID, Data: data})
}

// Readables returns the stored readable sidecars for docID's given commits,
// keyed by commit id; commits without one are absent. A backend without the
// changelog.Annotator capability returns nil, nil.
func (k *Kit) Readables(ctx context.Context, docID string, commitIDs []string) (map[string]chronicleexplain.Readable, error) {
	if k.ann == nil {
		return nil, nil
	}
	raw, err := k.ann.LoadAnnotations(ctx, docID, commitIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[string]chronicleexplain.Readable, len(raw))
	for id, data := range raw {
		var r chronicleexplain.Readable
		if err := json.Unmarshal(data, &r); err != nil {
			r = chronicleexplain.Readable{Error: fmt.Sprintf("unreadable sidecar: %v", err)}
		}
		out[id] = r
	}
	return out, nil
}

// Explain returns docID's full chain (oldest first) alongside display-decorated
// rows for every commit, derived live under opts — with the name-resolution
// fields (Display, Element.Name) overlaid from stored readable sidecars where
// present. Stored names win because they were resolved at seal time, when
// external referents still existed; commits without a sidecar (pre-feature
// history, direct RecordChanges seals, capability-less backends) decorate live
// only. Labels and trails always render live — relabeling history is the
// schema's job; losing referent names was the bug the sidecar fixes.
func (k *Kit) Explain(ctx context.Context, docID string, opts ...chronicleschema.Option) ([]changelog.Commit, [][]chronicleexplain.Explained, error) {
	commits, err := k.svc.Commits(ctx, docID, 0) // newest first
	if err != nil {
		return nil, nil, err
	}
	slices.Reverse(commits)
	rows, err := chronicleexplain.Explain(commits, opts...)
	if err != nil {
		return nil, nil, err
	}
	if k.ann != nil && len(commits) > 0 {
		ids := make([]string, len(commits))
		for i, c := range commits {
			ids[i] = c.ID
		}
		// ponytail: a sidecar-store error degrades to live-only decoration —
		// the overlay is garnish; the replay is the complete answer.
		if stored, err := k.Readables(ctx, docID, ids); err == nil {
			for i, c := range commits {
				overlayReadable(rows[i], stored[c.ID])
			}
		}
	}
	return commits, rows, nil
}

// overlayReadable overwrites the rows' name-resolution fields from a stored
// sidecar. A payload carrying an error, or whose row count does not align with
// the sealed changes (older or foreign writer), is skipped whole — live
// rendering stands. Within an aligned payload, stored wins where it spoke and
// live fills its silences.
func overlayReadable(rows []chronicleexplain.Explained, r chronicleexplain.Readable) {
	if r.Error != "" || len(r.Rows) != len(rows) {
		return
	}
	for i := range rows {
		if d := r.Rows[i].Display; d != nil {
			rows[i].Display = d
		}
		if e := r.Rows[i].Element; e != nil && e.Name != "" && rows[i].Element != nil {
			rows[i].Element.Name = e.Name
		}
	}
}
