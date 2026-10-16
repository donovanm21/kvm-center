package main

import (
	"encoding/json"
	"io"
	"kvm-center/internal/models"
	"net/http"
	"strings"
	"time"
)

func commandResolutionRoute(store *Store) http.Handler {
	return requirePerm(store, "user.manage", handleResolveCommand(store))
}

// resolveUnknown checks and transitions under one lock. Retries must repeat
// the same authenticated actor, outcome and note.
func (l *CommandLog) resolveUnknown(id, outcome, note, actor, token string) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[id]
	if e == nil {
		return false, http.StatusNotFound
	}
	if e.Resolution != nil {
		r := e.Resolution
		if e.Outcome == outcome && r.Note == note && r.ActorID == actor && r.TokenID == token {
			if l.resolving == nil {
				l.resolving = make(map[string]bool)
			}
			l.resolving[id] = true
			return false, http.StatusOK
		}
		return false, http.StatusConflict
	}
	if e.Outcome != "unknown" {
		return false, http.StatusConflict
	}
	now := time.Now().UTC()
	e.Resolution = &models.CommandResolution{ActorID: actor, TokenID: token, Note: note, At: now}
	e.Outcome, e.Message, e.StatusCode, e.Replayed = outcome, note, 0, false
	e.AppliedAt = &now
	expiry := now.Add(commandLogRetentionDays * 24 * time.Hour)
	e.ExpiresAt = &expiry
	if l.resolving == nil {
		l.resolving = make(map[string]bool)
	}
	l.resolving[id] = true
	return true, http.StatusOK
}

// POST /api/admin/command-log/resolve records an operator observation without
// resending a command. A failed save leaves the observation protected in memory;
// the identical request retries persistence without rewriting history.
func handleResolveCommand(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		actor := userFromRequest(r)
		if actor == nil || actor.ID == "" {
			writeError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		var req struct {
			RequestID string `json:"request_id"`
			Outcome   string `json:"outcome"`
			Note      string `json:"note"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid resolution JSON")
			return
		}
		if err := dec.Decode(new(any)); err != io.EOF {
			writeError(w, http.StatusBadRequest, "expected one JSON object")
			return
		}
		req.RequestID, req.Note = strings.TrimSpace(req.RequestID), strings.TrimSpace(req.Note)
		if req.RequestID == "" || len(req.RequestID) > 256 || req.Note == "" || len(req.Note) > 4096 || (req.Outcome != "success" && req.Outcome != "failed") {
			writeError(w, http.StatusBadRequest, "request_id, success or failed outcome, and an observation note (up to 4096 bytes) are required")
			return
		}
		if store.commandLog == nil || store.backend == nil || !store.isSaveGated() {
			writeError(w, http.StatusServiceUnavailable, "command resolution requires available persistence")
			return
		}
		l := store.commandLog
		l.resolveMu.Lock()
		defer l.resolveMu.Unlock()
		changed, status := l.resolveUnknown(req.RequestID, req.Outcome, req.Note, actor.ID, tokenIDFromRequest(r))
		if status != http.StatusOK {
			writeError(w, status, "command not found or resolution conflicts with recorded state")
			return
		}
		if changed {
			audit(store, r, "command.resolved", "request/"+req.RequestID, map[string]string{"outcome": req.Outcome, "note": req.Note}, true, "")
		}
		if err := store.SaveChecked(); err != nil {
			writeError(w, http.StatusServiceUnavailable, "resolution durability is unconfirmed; retry the identical request")
			return
		}
		l.mu.Lock()
		delete(l.resolving, req.RequestID)
		l.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"status": "resolved", "request_id": req.RequestID, "outcome": req.Outcome})
	}
}
