// Package httpapi is the optional HTTP transport chronicle/core deliberately
// omits: a stdlib http.Handler over a changelog.Service, using chroniclekit to
// diff-and-seal. Mount it where you like; auth, middleware, and TLS are yours.
package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
	chroniclekit "github.com/zdirnecamlcs96/chronicle/kit"
)

// Handler returns an http.Handler exposing the changelog over svc:
//
//	POST /commits          {doc_id, changes[]? | before?,after?, message?, idempotency_key?}
//	GET  /commits?doc=&limit=    a document's commits (or all documents when doc is omitted)
//	GET  /commits/{id}     one commit by id
//	GET  /changes?doc=&limit=    the flattened change feed
func Handler(svc changelog.Service) http.Handler {
	k := chroniclekit.New(svc)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /commits", func(w http.ResponseWriter, r *http.Request) { postCommits(k, w, r) })
	mux.HandleFunc("GET /commits/{id}", func(w http.ResponseWriter, r *http.Request) { getCommit(svc, w, r) })
	mux.HandleFunc("GET /commits", func(w http.ResponseWriter, r *http.Request) { listCommits(svc, w, r) })
	mux.HandleFunc("GET /changes", func(w http.ResponseWriter, r *http.Request) { listChanges(svc, w, r) })
	return mux
}

type postRequest struct {
	DocID          string             `json:"doc_id"`
	Changes        []changelog.Change `json:"changes"`
	Before         json.RawMessage    `json:"before"`
	After          json.RawMessage    `json:"after"`
	Message        string             `json:"message"`
	IdempotencyKey string             `json:"idempotency_key"`
}

func postCommits(k *chroniclekit.Kit, w http.ResponseWriter, r *http.Request) {
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
	if req.IdempotencyKey != "" {
		opts = append(opts, chroniclekit.WithIdempotencyKey(req.IdempotencyKey))
	}

	var (
		commit changelog.Commit
		err    error
	)
	switch {
	case len(req.Changes) > 0:
		commit, err = k.RecordChanges(r.Context(), req.DocID, req.Changes, opts...)
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
		writeError(w, http.StatusBadRequest, "provide changes, or before/after")
		return
	}

	if errors.Is(err, changelog.ErrEmptyChanges) {
		writeError(w, http.StatusBadRequest, "nothing to commit")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
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
		writeError(w, http.StatusInternalServerError, err.Error())
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
	if doc == "" {
		all, err := svc.AllCommits(r.Context(), limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
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
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cs)
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
			writeError(w, http.StatusInternalServerError, err.Error())
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
			writeError(w, http.StatusInternalServerError, err.Error())
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
