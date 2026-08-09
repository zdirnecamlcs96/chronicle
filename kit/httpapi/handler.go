// Package httpapi is the optional HTTP transport chronicle/core deliberately
// omits: a stdlib http.Handler over a changelog.Service, using chroniclekit to
// diff-and-seal. Mount it where you like; auth, middleware, and TLS are yours.
package httpapi

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
	chroniclekit "github.com/zdirnecamlcs96/chronicle/kit"
	chroniclediff "github.com/zdirnecamlcs96/chronicle/kit/diff"
	chronicleexplain "github.com/zdirnecamlcs96/chronicle/kit/explain"
	chronicleschema "github.com/zdirnecamlcs96/chronicle/kit/schema"
)

// maxBody caps the accepted request body; a changelog commit is small, so a
// few MB is generous and stops a multi-GB POST from being buffered into memory.
const maxBody = 4 << 20

// Handler returns an http.Handler exposing the changelog over svc:
//
//	POST /commits          {doc_id, changes[]? | patch[]? | before?,after?, schema?, message?, actor?, idempotency_key?}
//	GET  /commits?doc=&limit=&after=    a document's commits (or all documents when doc is omitted);
//	                                     after=<commit id> returns the tail strictly after it, oldest-first
//	GET  /commits/{id}     one commit by id
//	GET  /changes?doc=&limit=    the flattened change feed
//	GET  /state?doc=&at=    a document's state at HEAD, or as of commit at
//	GET  /verify?doc=    verify a document's hash chain
//	POST /explain          {doc, limit?, options?} → commits with display decoration
func Handler(svc changelog.Service) http.Handler {
	k := chroniclekit.NewWithService(svc)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /commits", func(w http.ResponseWriter, r *http.Request) { postCommits(k, w, r) })
	mux.HandleFunc("GET /commits/{id}", func(w http.ResponseWriter, r *http.Request) { getCommit(svc, w, r) })
	mux.HandleFunc("GET /commits", func(w http.ResponseWriter, r *http.Request) { listCommits(svc, w, r) })
	mux.HandleFunc("GET /changes", func(w http.ResponseWriter, r *http.Request) { listChanges(svc, w, r) })
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) { getState(k, w, r) })
	mux.HandleFunc("GET /verify", func(w http.ResponseWriter, r *http.Request) { getVerify(svc, w, r) })
	mux.HandleFunc("POST /explain", func(w http.ResponseWriter, r *http.Request) { postExplain(svc, w, r) })
	return mux
}

// postRequest carries the three write shapes. They are tried in the order the
// switch below lists them — changes, patch, before/after — so a body naming
// more than one is not an error, it just uses the first.
type postRequest struct {
	DocID          string                   `json:"doc_id"`
	Changes        []changelog.Change       `json:"changes"`
	Patch          []chroniclekit.Operation `json:"patch"`
	Before         json.RawMessage          `json:"before"`
	After          json.RawMessage          `json:"after"`
	Schema         writeOptions             `json:"schema"`
	Message        string                   `json:"message"`
	Actor          string                   `json:"actor"`
	IdempotencyKey string                   `json:"idempotency_key"`
}

// writeOptions is the write half of the chronicleschema vocabulary — the half
// that survives a JSON boundary, mirroring explainOptions on the read side.
// Identity shapes what is RECORDED and cannot be re-declared after a commit is
// sealed, so a producer writing over HTTP must be able to state it in the same
// request; there is no second chance. WithValueTypes is deliberately absent:
// ValueType.Canon is a Go func, so that one shape cannot cross a wire at all.
type writeOptions struct {
	ArrayKeys      map[string]string `json:"array_keys"`
	IdentityFields []string          `json:"identity_fields"`
	StrictIdentity bool              `json:"strict_identity"`
}

func (o writeOptions) schemaOptions() []chronicleschema.Option {
	var opts []chronicleschema.Option
	if len(o.ArrayKeys) > 0 {
		opts = append(opts, chronicleschema.WithArrayKeys(o.ArrayKeys))
	}
	if len(o.IdentityFields) > 0 {
		opts = append(opts, chronicleschema.WithIdentityFields(o.IdentityFields...))
	}
	if o.StrictIdentity {
		opts = append(opts, chronicleschema.WithStrictIdentity())
	}
	return opts
}

func postCommits(k *chroniclekit.Kit, w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	var req postRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.DocID == "" {
		writeError(w, http.StatusBadRequest, "doc_id is required")
		return
	}
	var opts []chroniclekit.RecordOption
	if req.Message != "" {
		opts = append(opts, chroniclekit.WithMessage(req.Message))
	}
	if req.Actor != "" {
		opts = append(opts, chroniclekit.WithActor(req.Actor))
	}
	if req.IdempotencyKey != "" {
		opts = append(opts, chroniclekit.WithIdempotencyKey(req.IdempotencyKey))
	}
	// Ignored by the changes path, which does no diff.
	if so := req.Schema.schemaOptions(); len(so) > 0 {
		opts = append(opts, chroniclekit.WithDiffOptions(so...))
	}

	var (
		commit changelog.Commit
		err    error
	)
	switch {
	case len(req.Changes) > 0:
		commit, err = k.RecordChanges(r.Context(), req.DocID, req.Changes, opts...)
	case len(req.Patch) > 0:
		// RecordPatch, not RecordChanges(ToChanges(...)): it applies the patch to
		// the stored state and diffs the result, so the sealed changes carry From
		// and are keyed by identity rather than by the client's array indices.
		commit, err = k.RecordPatch(r.Context(), req.DocID, req.Patch, opts...)
	case req.After != nil:
		// Diff path: after is the new state. An omitted before means "create from
		// empty"; before-only is rejected below (it would silently delete the doc).
		var before, after any
		if req.Before != nil {
			if e := json.Unmarshal(req.Before, &before); e != nil {
				writeError(w, http.StatusBadRequest, "invalid before")
				return
			}
		}
		if e := json.Unmarshal(req.After, &after); e != nil {
			writeError(w, http.StatusBadRequest, "invalid after")
			return
		}
		commit, err = k.RecordUpdate(r.Context(), req.DocID, before, after, opts...)
	case req.Before != nil:
		writeError(w, http.StatusBadRequest, "after is required for a diff (omit before to create; use changes to delete)")
		return
	default:
		writeError(w, http.StatusBadRequest, "provide changes, patch, or before/after")
		return
	}

	if errors.Is(err, changelog.ErrEmptyChanges) {
		writeError(w, http.StatusBadRequest, "nothing to commit")
		return
	}
	// Both name the offending op or array in kit vocabulary only, so echoing the
	// text discloses nothing about the backend. Both are caused by what the
	// client sent — an unsealable op, or a document whose arrays do not satisfy
	// the identity it declared — so both are 400.
	if errors.Is(err, chroniclekit.ErrUnsupportedOp) || errors.Is(err, chroniclediff.ErrNoIdentity) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, commit)
}

// docCommitResponse gives DocCommit explicit snake_case JSON tags so every
// endpoint's output is consistently snake_case (core.DocCommit has none).
type docCommitResponse struct {
	DocID  string           `json:"doc_id"`
	Commit changelog.Commit `json:"commit"`
}

func toDocCommitResponse(dc changelog.DocCommit) docCommitResponse {
	return docCommitResponse{DocID: dc.DocID, Commit: dc.Commit}
}

func getCommit(svc changelog.Service, w http.ResponseWriter, r *http.Request) {
	dc, ok, err := svc.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		serverError(w, err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "commit not found")
		return
	}
	writeJSON(w, http.StatusOK, toDocCommitResponse(dc))
}

func listCommits(svc changelog.Service, w http.ResponseWriter, r *http.Request) {
	limit := queryLimit(r)
	doc := r.URL.Query().Get("doc")
	after := r.URL.Query().Get("after")
	if after != "" {
		if doc == "" {
			writeError(w, http.StatusBadRequest, "after requires doc")
			return
		}
		cs, err := commitsAfter(svc, r, doc, after, limit)
		if errors.Is(err, changelog.ErrNoSuchCommit) {
			writeError(w, http.StatusNotFound, "unknown after commit")
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, cs)
		return
	}
	if doc == "" {
		all, err := svc.AllCommits(r.Context(), limit)
		if err != nil {
			serverError(w, err)
			return
		}
		rows := make([]docCommitResponse, len(all))
		for i, dc := range all {
			rows[i] = toDocCommitResponse(dc)
		}
		writeJSON(w, http.StatusOK, rows)
		return
	}
	cs, err := svc.Commits(r.Context(), doc, limit)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cs)
}

// commitsAfter returns doc's commits strictly after afterID, OLDEST first —
// cursor/tail semantics, deliberately opposite listCommits' default
// newest-first order, so a caller can advance the cursor by the last id seen.
// It prefers the backend's TailReader (O(commits since after)); a backend
// without one falls back to a full Commits read reversed into chronological
// order. Both paths return changelog.ErrNoSuchCommit when afterID is not on
// the document.
func commitsAfter(svc changelog.Service, r *http.Request, doc, after string, limit int) ([]changelog.Commit, error) {
	if tr := tailReaderFor(svc); tr != nil {
		return tr.CommitsAfter(r.Context(), doc, after, limit)
	}
	cs, err := svc.Commits(r.Context(), doc, 0)
	if err != nil {
		return nil, err
	}
	slices.Reverse(cs) // oldest-first
	idx := -1
	for i, c := range cs {
		if c.ID == after {
			idx = i
			break
		}
	}
	if idx == -1 {
		return nil, changelog.ErrNoSuchCommit
	}
	cs = cs[idx+1:]
	if limit > 0 && limit < len(cs) {
		cs = cs[:limit]
	}
	return cs, nil
}

// tailReaderFor walks svc's Unwrap() chain — the same walk chronicleview.New
// (kit/view/view.go) uses to detect the backend's optional capabilities —
// looking for one that exposes changelog.TailReader. nil means none does.
func tailReaderFor(svc changelog.Service) changelog.TailReader {
	var l changelog.Log
	if u, ok := svc.(interface{ Unwrap() changelog.Log }); ok {
		l = u.Unwrap()
	}
	for l != nil {
		if tr, ok := l.(changelog.TailReader); ok {
			return tr
		}
		u, ok := l.(interface{ Unwrap() changelog.Log })
		if !ok {
			break
		}
		l = u.Unwrap()
	}
	return nil
}

// changeRow is one row of the flattened change feed: a Change plus the commit (and
// document) it belongs to.
type changeRow struct {
	CommitID string `json:"commit_id"`
	DocID    string `json:"doc_id,omitempty"`
	changelog.Change
}

func listChanges(svc changelog.Service, w http.ResponseWriter, r *http.Request) {
	limit := queryLimit(r)
	doc := r.URL.Query().Get("doc")
	rows := []changeRow{}
	if doc == "" {
		all, err := svc.AllCommits(r.Context(), limit)
		if err != nil {
			serverError(w, err)
			return
		}
		for _, dc := range all {
			for _, ch := range dc.Commit.Changes {
				rows = append(rows, changeRow{CommitID: dc.Commit.ID, DocID: dc.DocID, Change: ch})
			}
		}
	} else {
		cs, err := svc.Commits(r.Context(), doc, limit)
		if err != nil {
			serverError(w, err)
			return
		}
		for _, c := range cs {
			for _, ch := range c.Changes {
				rows = append(rows, changeRow{CommitID: c.ID, DocID: doc, Change: ch})
			}
		}
	}
	writeJSON(w, http.StatusOK, rows)
}

func getState(k *chroniclekit.Kit, w http.ResponseWriter, r *http.Request) {
	doc := r.URL.Query().Get("doc")
	if doc == "" {
		writeError(w, http.StatusBadRequest, "doc is required")
		return
	}
	at := r.URL.Query().Get("at")
	var (
		state map[string]any
		err   error
	)
	if at == "" {
		state, err = k.State(r.Context(), doc)
	} else {
		state, err = k.StateAt(r.Context(), doc, at)
	}
	if err != nil {
		// StateAt's unknown-commit error (kit/view/view.go's stateUpTo) has no
		// sentinel to match on — it is client input (a bad at=), so it is
		// recognized by its distinguishing text rather than disclosed verbatim.
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, "unknown at commit")
			return
		}
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// verifyResponse reports changelog.VerifyChain's result for a document,
// snake_case like every other route. Head is the newest commit id (empty when
// the document has no commits). Error is VerifyChain's own text — chain
// vocabulary only (hash mismatch, broken chain, fork, authors mismatch) — so
// it discloses no backend internals.
type verifyResponse struct {
	OK      bool   `json:"ok"`
	Commits int    `json:"commits"`
	Head    string `json:"head,omitempty"`
	Error   string `json:"error,omitempty"`
}

func getVerify(svc changelog.Service, w http.ResponseWriter, r *http.Request) {
	doc := r.URL.Query().Get("doc")
	if doc == "" {
		writeError(w, http.StatusBadRequest, "doc is required")
		return
	}
	commits, err := svc.Commits(r.Context(), doc, 0)
	if err != nil {
		serverError(w, err)
		return
	}
	// Verification executed and produced a result; that is not a transport
	// failure, so the response is always 200 — success or chain failure is
	// carried in the body, not the status.
	if verr := changelog.VerifyChain(commits); verr != nil {
		writeJSON(w, http.StatusOK, verifyResponse{Commits: len(commits), Error: verr.Error()})
		return
	}
	var head string
	if len(commits) > 0 {
		head = commits[0].ID // newest-first
	}
	writeJSON(w, http.StatusOK, verifyResponse{OK: true, Commits: len(commits), Head: head})
}

// explainRequest asks for a document's history decorated for display. Options
// carries the read-side half of the chronicleschema vocabulary — the half that
// survives a JSON boundary. WithLabels is deliberately absent: it is a Go func,
// and a client that wants its own labels translates from each row's Path (the
// raw dotted path, which every row carries) instead of the Title Case default.
// WithValueTypes is absent because it only shapes writes; Explain never reads it.
type explainRequest struct {
	Doc     string         `json:"doc"`
	Limit   int            `json:"limit"`
	Options explainOptions `json:"options"`
}

type explainOptions struct {
	ArrayKeys      map[string]string `json:"array_keys"`
	IdentityFields []string          `json:"identity_fields"`
	NameFields     []string          `json:"name_fields"`
	IgnoredFields  []string          `json:"ignored_fields"`
	Names          map[string]string `json:"names"`
}

// schemaOptions maps the request onto the kit vocabulary. Empty fields are
// skipped rather than passed through: WithNameFields and WithIdentityFields
// REPLACE their configuration, so forwarding an empty slice would wipe the
// default name field instead of leaving it alone.
func (o explainOptions) schemaOptions() []chronicleschema.Option {
	var opts []chronicleschema.Option
	if len(o.ArrayKeys) > 0 {
		opts = append(opts, chronicleschema.WithArrayKeys(o.ArrayKeys))
	}
	if len(o.IdentityFields) > 0 {
		opts = append(opts, chronicleschema.WithIdentityFields(o.IdentityFields...))
	}
	if len(o.NameFields) > 0 {
		opts = append(opts, chronicleschema.WithNameFields(o.NameFields...))
	}
	if len(o.IgnoredFields) > 0 {
		opts = append(opts, chronicleschema.WithIgnoredFields(o.IgnoredFields...))
	}
	if len(o.Names) > 0 {
		opts = append(opts, chronicleschema.WithNames(o.Names))
	}
	return opts
}

// explainedCommit is one commit whose Changes carry display decoration. The
// stored Changes are not repeated alongside them — each Explained embeds the
// change it decorates, untouched.
type explainedCommit struct {
	ID      string                       `json:"id"`
	Parent  string                       `json:"parent"`
	At      time.Time                    `json:"at"`
	Authors []string                     `json:"authors"`
	Message string                       `json:"message,omitempty"`
	Changes []chronicleexplain.Explained `json:"changes"`
}

func postExplain(svc changelog.Service, w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	var req explainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Doc == "" {
		writeError(w, http.StatusBadRequest, "doc is required")
		return
	}

	// Always fetch the WHOLE chain: Explain derives its decoration by replaying
	// from the root, so a truncated history would silently yield wrong labels,
	// names, and element identity. Limit trims the response, never the replay.
	commits, err := svc.Commits(r.Context(), req.Doc, 0)
	if err != nil {
		serverError(w, err)
		return
	}
	slices.Reverse(commits) // Commits is newest-first; Explain replays oldest-first
	rows, err := chronicleexplain.Explain(commits, req.Options.schemaOptions()...)
	if err != nil {
		serverError(w, err)
		return
	}

	out := make([]explainedCommit, len(commits))
	for i, c := range commits {
		out[i] = explainedCommit{
			ID: c.ID, Parent: c.Parent, At: c.At,
			Authors: c.Authors, Message: c.Message, Changes: rows[i],
		}
	}
	slices.Reverse(out) // answer newest-first, like every other read route
	if req.Limit > 0 && req.Limit < len(out) {
		out = out[:req.Limit]
	}
	writeJSON(w, http.StatusOK, out)
}

// queryLimit reads ?limit=; an absent or unparseable value means 0 (unbounded),
// matching the Service contract where limit<=0 returns all.
func queryLimit(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil {
		return 0
	}
	return n
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// serverError logs the real error and returns a generic 500 so backend/adapter
// internals (SQL text, table names, chain internals) are not disclosed to callers.
func serverError(w http.ResponseWriter, err error) {
	log.Printf("httpapi: %v", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}
