package main

import (
	"encoding/json"
	"net/http"

	"kvm-center/internal/models"
)

// GET /api/settings — any authenticated user can read the global feature
// flags, because the UI has to know which buttons to grey out.
//
// POST /api/settings — admin only (wrapped by requirePerm at route
// registration time) replaces the full feature set.
func handleFeaturesGet(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, store.GetFeatures())
	}
}

func handleFeaturesUpdate(store *Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodPut {
			writeError(w, http.StatusMethodNotAllowed, "POST / PUT only")
			return
		}
		var f models.Features
		if err := json.NewDecoder(r.Body).Decode(&f); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		store.SetFeatures(f)
		audit(store, r, AuditFeatures, "features", map[string]string{
			"disable_vm_create":      boolToStr(f.DisableVMCreate),
			"disable_vm_delete":      boolToStr(f.DisableVMDelete),
			"disable_snapshots":      boolToStr(f.DisableSnapshots),
			"disable_console":        boolToStr(f.DisableConsole),
			"disable_migrations":     boolToStr(f.DisableMigrations),
			"disable_storage_upload": boolToStr(f.DisableStorageUpload),
			"disable_storage_delete": boolToStr(f.DisableStorageDelete),
		}, true, "")
		writeJSON(w, http.StatusOK, f)
	})
}

// featureBlocked returns a 403 with a short explanation and true when
// the named feature is disabled. Call from the top of an endpoint:
//
//   if featureBlocked(w, store, "vm_create", "creating VMs is disabled") { return }
func featureBlocked(w http.ResponseWriter, store *Store, key, label string) bool {
	f := store.GetFeatures()
	var off bool
	switch key {
	case "vm_create":
		off = f.DisableVMCreate
	case "vm_delete":
		off = f.DisableVMDelete
	case "snapshots":
		off = f.DisableSnapshots
	case "console":
		off = f.DisableConsole
	case "migrations":
		off = f.DisableMigrations
	case "storage_upload":
		off = f.DisableStorageUpload
	case "storage_delete":
		off = f.DisableStorageDelete
	}
	if off {
		writeError(w, http.StatusForbidden, "feature disabled by administrator: "+label)
	}
	return off
}
