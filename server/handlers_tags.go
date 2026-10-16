package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"kvm-center/internal/models"
)

// Response-only overlay: neither cached agent inventory nor its persisted
// model acquires operator-owned tag fields.
type vmTagView struct {
	*models.VM
	Tags []string `json:"tags"`
}

func tagView(s *Store, vm *models.VM) vmTagView {
	copy := *vm
	tags := s.GetVMTags(vm.UUID)
	if tags == nil {
		tags = []string{}
	}
	return vmTagView{&copy, tags}
}
func vmDetailRoute(s *Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/tags") {
			perm := "vm.view"
			if r.Method != http.MethodGet {
				perm = "vm.edit"
			}
			requirePerm(s, perm, handleVMTags(s)).ServeHTTP(w, r)
			return
		}
		handleGetVMByUUID(s).ServeHTTP(w, r)
	})
}

// PUT is a conditional replacement. Repeat the identical request after a 503:
// the edit may be staged or persisted already. A different stale edit conflicts.
func handleVMTags(s *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/vms/"), "/tags"))
		if !tagUUIDPattern.MatchString(id) {
			writeError(w, 400, "invalid VM UUID")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodPut {
			w.Header().Set("Allow", "GET, PUT")
			writeError(w, 405, "GET or PUT only")
			return
		}
		if s.GetVMByUUID(id) == nil {
			writeError(w, 404, "VM not found in current inventory")
			return
		}
		if r.Method == http.MethodGet {
			tags := s.GetVMTags(id)
			if tags == nil {
				tags = []string{}
			}
			writeJSON(w, 200, map[string]any{"tags": tags})
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
		if err != nil || uniqueJSONKeys(raw) != nil {
			writeError(w, 400, "invalid tag JSON")
			return
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || len(fields) != 2 {
			writeError(w, 400, "tags and expected_tags arrays are required")
			return
		}
		var desired, expected []string
		if json.Unmarshal(fields["tags"], &desired) != nil || json.Unmarshal(fields["expected_tags"], &expected) != nil || desired == nil || expected == nil {
			writeError(w, 400, "tags and expected_tags must be arrays, not null")
			return
		}
		desired, err = normalizeVMTags(desired)
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
		expected, err = normalizeVMTags(expected)
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if s.tagEditMu.LockContext(ctx) != nil {
			writeError(w, 503, "tag edit busy; retry the identical request")
			return
		}
		defer s.tagEditMu.Unlock()
		if s.backend == nil || !s.isSaveGated() || !s.saves.begin() {
			writeError(w, 503, "tag persistence unavailable")
			return
		}
		defer s.saves.end()
		current := s.GetVMTags(id)
		if !slices.Equal(current, expected) && !slices.Equal(current, desired) {
			writeError(w, 409, "tags changed; reload and review before saving")
			return
		}
		if !slices.Equal(current, desired) {
			if err := s.SetVMTags(id, desired); err != nil {
				writeError(w, 400, err.Error())
				return
			}
			audit(s, r, "vm.tags.updated", "vm/"+id, map[string]string{"tags": strings.Join(desired, ",")}, true, "")
		}
		if s.saveCheckedContext(ctx) != nil {
			writeError(w, 503, "tag durability is unconfirmed; retry the identical request")
			return
		}
		writeJSON(w, 200, map[string]any{"tags": desired})
	}
}

// Tags are created by assignment. Removing their final assignment removes them
// from this catalog; no separate global definition or delete operation exists.
func handleTags(s *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, 405, "GET only")
			return
		}
		counts := map[string]int{}
		s.mu.RLock()
		for _, tags := range s.vmTags {
			for _, tag := range tags {
				counts[tag]++
			}
		}
		s.mu.RUnlock()
		type row struct {
			Tag   string `json:"tag"`
			Count int    `json:"count"`
		}
		rows := []row{}
		for tag, count := range counts {
			rows = append(rows, row{tag, count})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].Tag < rows[j].Tag })
		writeJSON(w, 200, map[string]any{"tags": rows})
	}
}
