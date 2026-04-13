package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"kvm-center/internal/models"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("handlers: encode error: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// POST /api/telemetry — agents call this every N seconds.
func handleTelemetry(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}

		var payload models.TelemetryPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}

		// Derive agent IP from request if not provided in payload.
		if payload.IP == "" {
			payload.IP = clientIP(r)
		}
		if payload.Timestamp.IsZero() {
			payload.Timestamp = time.Now()
		}
		if payload.AgentPort == 0 {
			payload.AgentPort = 8081
		}

		store.ApplyTelemetry(&payload)
		log.Printf("telemetry: %s  host=%s  vms=%d  cpu=%.1f%%  mem=%.1f%%",
			payload.IP, payload.HostID, len(payload.VMs), payload.CPUUsage, payload.MemUsage)

		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// GET /api/hosts — list all known hypervisors.
func handleGetHosts(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		writeJSON(w, http.StatusOK, store.GetHosts())
	}
}

// GET /api/hosts/{id} — single host detail.
func handleGetHost(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/hosts/")
		h := store.GetHost(id)
		if h == nil {
			writeError(w, http.StatusNotFound, "host not found")
			return
		}
		writeJSON(w, http.StatusOK, h)
	}
}

// GET /api/vms[?host=<id>&search=<q>] — list VMs, optionally filtered.
func handleGetVMs(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		hostID := r.URL.Query().Get("host")
		search := strings.ToLower(r.URL.Query().Get("search"))

		vms := store.GetVMs(hostID)
		if search != "" {
			var filtered []*models.VM
			for _, v := range vms {
				if strings.Contains(strings.ToLower(v.Name), search) ||
					strings.Contains(strings.ToLower(v.UUID), search) ||
					strings.Contains(strings.ToLower(v.HostID), search) {
					filtered = append(filtered, v)
				}
			}
			vms = filtered
		}
		if vms == nil {
			vms = []*models.VM{}
		}
		writeJSON(w, http.StatusOK, vms)
	}
}

// GET /api/storage[?host=<id>] — list storage pools.
func handleGetStorage(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		hostID := r.URL.Query().Get("host")
		pools := store.GetStorage(hostID)
		if pools == nil {
			pools = []*models.StoragePool{}
		}
		writeJSON(w, http.StatusOK, pools)
	}
}

// POST /api/command — proxy a lifecycle command to the target agent.
func handleCommand(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}

		var req models.APICommandRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}

		action := strings.ToUpper(req.Action)
		var needPerm string
		switch action {
		case "START", "STOP", "FORCESTOP", "RESTART":
			needPerm = models.PermVMLifecycle
		case "SNAPSHOT", "REVERT_SNAPSHOT", "DELETE_SNAPSHOT":
			needPerm = models.PermVMSnapshot
		case "MIGRATE", "OFFLINE_MIGRATE":
			needPerm = models.PermVMMigrate
		case "DELETE_VM":
			needPerm = models.PermVMDelete
		default:
			writeError(w, http.StatusBadRequest, "unknown action: "+req.Action)
			return
		}

		// RBAC check: the caller's role must grant the matching permission.
		u := userFromRequest(r)
		if u == nil {
			writeError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		if !hasPermission(store, u, needPerm) {
			writeError(w, http.StatusForbidden, "missing permission: "+needPerm)
			return
		}

		// Global feature-flag gating. Lifecycle actions are never gated —
		// an admin must always be able to regain control of a VM.
		switch action {
		case "SNAPSHOT", "REVERT_SNAPSHOT", "DELETE_SNAPSHOT":
			if featureBlocked(w, store, "snapshots", "snapshots") {
				return
			}
		case "MIGRATE", "OFFLINE_MIGRATE":
			if featureBlocked(w, store, "migrations", "VM migrations") {
				return
			}
		case "DELETE_VM":
			if featureBlocked(w, store, "vm_delete", "deleting VMs") {
				return
			}
		}

		agentAddr := store.GetHostAgentAddr(req.HostID)
		if agentAddr == "" {
			writeError(w, http.StatusNotFound, "host not found or no agent address")
			return
		}

		agentCmd := models.CommandRequest{
			Action: action,
			VMName: req.VMName,
			VMUUID: req.VMUUID,
			Params: req.Params,
		}
		body, _ := json.Marshal(agentCmd)

		url := fmt.Sprintf("http://%s/command", agentAddr)
		log.Printf("command: %s → %s  vm=%s", action, agentAddr, req.VMName)

		// Track migrations in the store so the Migrations summary page
		// reflects in-flight + historical state without the React SPA
		// needing to POST /api/migrations/record explicitly.
		var migRec *models.MigrationRecord
		if action == "MIGRATE" || action == "OFFLINE_MIGRATE" {
			mode := "offline"
			if action == "MIGRATE" {
				mode = "live"
			}
			destIP := req.Params["destination"]
			migRec = &models.MigrationRecord{
				ID:         fmt.Sprintf("%s-%d", req.VMName, time.Now().UnixNano()),
				VMName:     req.VMName,
				SourceHost: req.HostID,
				DestHost:   resolveHostByIP(store, destIP),
				Mode:       mode,
				Status:     "running",
				StartedAt:  time.Now().Unix(),
				Phase:      "starting",
				Progress:   0,
			}
			store.AddMigration(migRec)
			if action == "OFFLINE_MIGRATE" {
				// Offline migrations run async on the agent; mirror the
				// /migrate-status feed onto the record until terminal.
				go pollMigrationStatus(store, migRec, agentAddr)
			}
		}

		// Migrations with copy-storage can take many minutes.
		timeout := 15 * time.Second
		if action == "MIGRATE" || action == "OFFLINE_MIGRATE" {
			timeout = 30 * time.Minute
		}
		client := &http.Client{Timeout: timeout}
		resp, err := client.Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			if migRec != nil {
				finalizeMigration(store, migRec, "failed", "agent unreachable: "+err.Error())
			}
			writeError(w, http.StatusBadGateway, "agent unreachable: "+err.Error())
			return
		}
		defer resp.Body.Close()

		respBody, _ := io.ReadAll(resp.Body)
		// Live migrations are synchronous in the agent — the response
		// arrives only when virsh migrate finishes (or errors). Offline
		// migrations return immediately and the poller takes over from
		// here.
		if migRec != nil && action == "MIGRATE" {
			if resp.StatusCode >= 400 {
				finalizeMigration(store, migRec, "failed", strings.TrimSpace(string(respBody)))
			} else {
				finalizeMigration(store, migRec, "complete", "live migration finished")
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(respBody)
	}
}

// resolveHostByIP returns the host ID matching the given IP, or the IP
// itself when no host is known. Used to fill the dest_host field on a
// migration record when the client only supplies a destination IP.
func resolveHostByIP(store *Store, ip string) string {
	if ip == "" {
		return ""
	}
	for _, h := range store.GetHosts() {
		if h.IP == ip {
			return h.ID
		}
	}
	return ip
}

// finalizeMigration stamps a record as complete or failed and re-publishes
// it through the store so SSE clients see the terminal state.
func finalizeMigration(store *Store, rec *models.MigrationRecord, status, detail string) {
	rec.Status = status
	rec.CompletedAt = time.Now().Unix()
	rec.ElapsedSec = rec.CompletedAt - rec.StartedAt
	if status == "complete" {
		rec.Progress = 100
		rec.Phase = "complete"
	} else {
		rec.Phase = "failed"
		rec.Error = detail
	}
	if detail != "" {
		rec.Detail = detail
	}
	store.AddMigration(rec)
}

// pollMigrationStatus polls the source agent's /migrate-status endpoint
// every second and mirrors phase/progress/detail onto the in-store
// migration record. Stops when the agent reports the migration is no
// longer Active, or after a 90-minute safety net.
func pollMigrationStatus(store *Store, rec *models.MigrationRecord, agentAddr string) {
	deadline := time.Now().Add(90 * time.Minute)
	client := &http.Client{Timeout: 4 * time.Second}
	url := fmt.Sprintf("http://%s/migrate-status", agentAddr)
	for time.Now().Before(deadline) {
		time.Sleep(1 * time.Second)
		resp, err := client.Get(url)
		if err != nil {
			continue
		}
		var s struct {
			Active   bool   `json:"active"`
			VMName   string `json:"vm_name"`
			Phase    string `json:"phase"`
			Progress int    `json:"progress"`
			Detail   string `json:"detail"`
			Error    string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&s)
		resp.Body.Close()
		// The agent reports a single most-recent migration — only mirror
		// updates that match this record's VM, otherwise a follow-up
		// migration on the same agent could clobber an unrelated record.
		if s.VMName != "" && s.VMName != rec.VMName {
			continue
		}
		if !s.Active {
			switch s.Phase {
			case "complete":
				finalizeMigration(store, rec, "complete", s.Detail)
			case "failed":
				detail := s.Error
				if detail == "" {
					detail = s.Detail
				}
				finalizeMigration(store, rec, "failed", detail)
			default:
				finalizeMigration(store, rec, "failed", "lost contact with agent migration status")
			}
			return
		}
		rec.Phase = s.Phase
		rec.Progress = s.Progress
		rec.Detail = s.Detail
		store.AddMigration(rec)
	}
	finalizeMigration(store, rec, "failed", "migration timed out after 90 minutes")
}

// POST /api/vms/notes — update the editable notes field for a VM, keyed by UUID.
func handleVMNotes(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var req struct {
			UUID  string `json:"uuid"`
			Notes string `json:"notes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if req.UUID == "" {
			writeError(w, http.StatusBadRequest, "uuid required")
			return
		}
		store.SetVMNotes(req.UUID, req.Notes)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// POST /api/guest-telemetry — receives a report from a kvm-tools daemon running in a guest.
func handleGuestTelemetry(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var p models.GuestTelemetryPayload
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if p.VMUUID == "" {
			writeError(w, http.StatusBadRequest, "vm_uuid required")
			return
		}
		store.ApplyGuestTelemetry(&p)
		log.Printf("guest-telemetry: uuid=%s host=%s os=%q", p.VMUUID, p.Hostname, p.OS)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// GET /api/vms/snapshots?host=<id>&vm=<name> — proxy the agent's snapshot list.
func handleVMSnapshots(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		hostID := r.URL.Query().Get("host")
		vmName := r.URL.Query().Get("vm")
		if hostID == "" || vmName == "" {
			writeError(w, http.StatusBadRequest, "host and vm query params required")
			return
		}

		agentAddr := store.GetHostAgentAddr(hostID)
		if agentAddr == "" {
			writeError(w, http.StatusNotFound, "host not found or no agent address")
			return
		}

		url := fmt.Sprintf("http://%s/snapshots/%s", agentAddr, vmName)
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Get(url)
		if err != nil {
			writeError(w, http.StatusBadGateway, "agent unreachable: "+err.Error())
			return
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(body)
	}
}

// GET /api/migrations — list all migration records.
func handleGetMigrations(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, store.GetMigrations())
	}
}

// POST /api/migrations — upsert a migration record (called by the command handler).
func handlePostMigration(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var rec models.MigrationRecord
		if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		store.AddMigration(&rec)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// GET /api/storage/browse?host=<id>&path=<dir> — proxy file listing from agent.
func handleStorageBrowse(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hostID := r.URL.Query().Get("host")
		path := r.URL.Query().Get("path")
		if hostID == "" {
			writeError(w, http.StatusBadRequest, "host required")
			return
		}
		agentAddr := store.GetHostAgentAddr(hostID)
		if agentAddr == "" {
			writeError(w, http.StatusNotFound, "host not found")
			return
		}
		url := fmt.Sprintf("http://%s/storage-browse?path=%s", agentAddr, url.QueryEscape(path))
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Get(url)
		if err != nil {
			writeError(w, http.StatusBadGateway, "agent unreachable")
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(body)
	}
}

// POST /api/storage/upload?host=<id>&path=<dir> — proxy file upload to agent.
func handleStorageUpload(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		if featureBlocked(w, store, "storage_upload", "storage uploads") {
			return
		}
		hostID := r.URL.Query().Get("host")
		path := r.URL.Query().Get("path")
		if hostID == "" || path == "" {
			writeError(w, http.StatusBadRequest, "host and path required")
			return
		}
		agentAddr := store.GetHostAgentAddr(hostID)
		if agentAddr == "" {
			writeError(w, http.StatusNotFound, "host not found")
			return
		}
		url := fmt.Sprintf("http://%s/storage-upload?path=%s", agentAddr, url.QueryEscape(path))
		client := &http.Client{Timeout: 30 * time.Minute}
		resp, err := client.Post(url, r.Header.Get("Content-Type"), r.Body)
		if err != nil {
			writeError(w, http.StatusBadGateway, "agent unreachable: "+err.Error())
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(body)
	}
}

// DELETE /api/storage/file?host=<id>&path=<file> — proxy file delete to agent.
func handleStorageDelete(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			writeError(w, http.StatusMethodNotAllowed, "DELETE only")
			return
		}
		if featureBlocked(w, store, "storage_delete", "storage file deletion") {
			return
		}
		hostID := r.URL.Query().Get("host")
		path := r.URL.Query().Get("path")
		if hostID == "" || path == "" {
			writeError(w, http.StatusBadRequest, "host and path required")
			return
		}
		agentAddr := store.GetHostAgentAddr(hostID)
		if agentAddr == "" {
			writeError(w, http.StatusNotFound, "host not found")
			return
		}
		url := fmt.Sprintf("http://%s/storage-file?path=%s", agentAddr, url.QueryEscape(path))
		req, _ := http.NewRequest(http.MethodDelete, url, nil)
		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			writeError(w, http.StatusBadGateway, "agent unreachable")
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(body)
	}
}

// GET /api/vm-create-info?host=<id> — proxy VM creation capabilities from agent.
func handleVMCreateInfo(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hostID := r.URL.Query().Get("host")
		if hostID == "" {
			writeError(w, http.StatusBadRequest, "host required")
			return
		}
		agentAddr := store.GetHostAgentAddr(hostID)
		if agentAddr == "" {
			writeError(w, http.StatusNotFound, "host not found")
			return
		}
		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Get(fmt.Sprintf("http://%s/vm-create-info", agentAddr))
		if err != nil {
			writeError(w, http.StatusBadGateway, "agent unreachable")
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(body)
	}
}

// POST /api/vm-create?host=<id> — proxy VM creation to agent.
func handleVMCreate(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		if featureBlocked(w, store, "vm_create", "creating VMs") {
			return
		}
		hostID := r.URL.Query().Get("host")
		if hostID == "" {
			writeError(w, http.StatusBadRequest, "host required")
			return
		}
		agentAddr := store.GetHostAgentAddr(hostID)
		if agentAddr == "" {
			writeError(w, http.StatusNotFound, "host not found")
			return
		}
		client := &http.Client{Timeout: 5 * time.Minute}
		resp, err := client.Post(
			fmt.Sprintf("http://%s/vm-create", agentAddr),
			"application/json", r.Body)
		if err != nil {
			writeError(w, http.StatusBadGateway, "agent unreachable: "+err.Error())
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(body)
	}
}

// GET /api/migrate-status?host=<id> — proxy the agent's migration progress.
func handleMigrateStatus(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hostID := r.URL.Query().Get("host")
		if hostID == "" {
			writeError(w, http.StatusBadRequest, "host query param required")
			return
		}
		agentAddr := store.GetHostAgentAddr(hostID)
		if agentAddr == "" {
			writeError(w, http.StatusNotFound, "host not found")
			return
		}
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get(fmt.Sprintf("http://%s/migrate-status", agentAddr))
		if err != nil {
			writeError(w, http.StatusBadGateway, "agent unreachable")
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(body)
	}
}

// GET /health — liveness probe.
func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// clientIP extracts the real client IP, honouring X-Forwarded-For.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.SplitN(xff, ",", 2)[0]
	}
	ip := r.RemoteAddr
	if idx := strings.LastIndex(ip, ":"); idx != -1 {
		ip = ip[:idx]
	}
	return strings.Trim(ip, "[]")
}
