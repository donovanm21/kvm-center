package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// Settings → Database management (v5.1). The UI consumes three
// endpoints:
//   GET  /api/settings/database           — current backend info
//   POST /api/settings/database/test      — probe a candidate Mongo URL
//                                           without swapping (operator
//                                           safety net before committing)
//   POST /api/settings/database/migrate   — copy data from one backend
//                                           to another (JSON ↔ Mongo)
// Switching the RUNNING backend is deliberately out of scope here —
// that requires either a server restart with the new -mongo-url or
// the operator flipping the env var. Keeps the hot-swap story simple
// and predictable.

func handleDatabaseStatus(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		info := store.backend.Info(ctx)
		writeJSON(w, http.StatusOK, info)
	}
}

func handleDatabaseTest() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var body struct {
			URL    string `json:"url"`
			DBName string `json:"db"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if body.URL == "" {
			writeError(w, http.StatusBadRequest, "url required")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		mb := NewMongoBackend(body.URL, body.DBName)
		defer mb.Close(context.Background())
		info := mb.Info(ctx)
		writeJSON(w, http.StatusOK, info)
	}
}

// handleDatabaseMigrate copies everything from the currently-active
// backend to a target backend defined by the caller. Common uses:
//   - Export to a JSON file before switching to Mongo.
//   - Manual re-seed after ops moves the data file around.
//   - "Make Mongo match JSON" during a split-brain recovery.
func handleDatabaseMigrate(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var body struct {
			To     string `json:"to"`     // "json" | "mongo"
			URL    string `json:"url"`    // required when to=mongo
			DBName string `json:"db"`
			Path   string `json:"path"`   // required when to=json
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		var dst Backend
		switch body.To {
		case "json":
			if body.Path == "" {
				writeError(w, http.StatusBadRequest, "path required for to=json")
				return
			}
			dst = NewJSONBackend(body.Path)
		case "mongo":
			if body.URL == "" {
				writeError(w, http.StatusBadRequest, "url required for to=mongo")
				return
			}
			dst = NewMongoBackend(body.URL, body.DBName)
			defer dst.Close(context.Background())
		default:
			writeError(w, http.StatusBadRequest, "to must be 'json' or 'mongo'")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		if err := migrateBackend(ctx, store.backend, dst); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ok",
			"from":   store.backend.Name(),
			"to":     dst.Name(),
		})
	}
}
