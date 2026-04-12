package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
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
		switch action {
		case "START", "STOP", "FORCESTOP", "RESTART", "SNAPSHOT", "REVERT_SNAPSHOT", "DELETE_SNAPSHOT", "MIGRATE", "OFFLINE_MIGRATE":
		default:
			writeError(w, http.StatusBadRequest, "unknown action: "+req.Action)
			return
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

		// Migrations with copy-storage can take many minutes.
		timeout := 15 * time.Second
		if action == "MIGRATE" || action == "OFFLINE_MIGRATE" {
			timeout = 30 * time.Minute
		}
		client := &http.Client{Timeout: timeout}
		resp, err := client.Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			writeError(w, http.StatusBadGateway, "agent unreachable: "+err.Error())
			return
		}
		defer resp.Body.Close()

		respBody, _ := io.ReadAll(resp.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(respBody)
	}
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
