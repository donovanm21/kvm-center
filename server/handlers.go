package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
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

		// Gossip the shared console-token secret so agents can verify
		// signatures minted by /api/vms/console-token. Ships on every
		// reply so a rotating secret (future feature) propagates
		// automatically within one telemetry tick.
		writeJSON(w, http.StatusOK, map[string]any{
			"status":         "ok",
			"console_secret": store.GetOrCreateConsoleSecret(),
		})
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

// DELETE /api/hosts/{id} — remove a hypervisor from the inventory
// (and its VM + storage rows + enrolment cert). Physical VMs on the
// host are not affected. Blocked when a migration is in-flight that
// references the host as source or destination. `host.manage` gated
// at route registration time.
func handleDeleteHost(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			writeError(w, http.StatusMethodNotAllowed, "DELETE only")
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/hosts/")
		h := store.GetHost(id)
		if h == nil {
			writeError(w, http.StatusNotFound, "host not found")
			return
		}
		// Safety: refuse while a migration references this host.
		for _, m := range store.GetMigrations() {
			if m == nil || m.Status != "running" {
				continue
			}
			if strings.EqualFold(m.SourceHost, id) || strings.EqualFold(m.DestHost, id) {
				writeError(w, http.StatusConflict,
					"refusing to delete — migration "+m.ID+" is in flight against this host; cancel it first")
				return
			}
		}
		vmCount := len(store.GetVMs(id))
		poolCount := len(store.GetStorage(id))
		store.DeleteHost(id)
		audit(store, r, AuditHostDelete, "host/"+id, map[string]string{
			"vm_rows":      strconv.Itoa(vmCount),
			"storage_rows": strconv.Itoa(poolCount),
		}, true, "")
		writeJSON(w, http.StatusOK, map[string]any{
			"deleted":      id,
			"vm_rows":      vmCount,
			"storage_rows": poolCount,
		})
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

// GET /api/vms/{uuid} — single VM by UUID (v5.33.7). Introduced so
// the Claude skill and any other client can fetch a VM without the
// overhead of pulling + filtering /api/vms. Case-insensitive UUID
// compare; 404 when the UUID isn't in any host's current inventory.
func handleGetVMByUUID(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/api/vms/")
		// Reject anything that still has a slash in it — the explicit
		// subpath handlers (snapshots/screenshot/…) should have matched
		// those. A slash-bearing remainder here means the caller asked
		// for an endpoint we don't implement yet.
		if rest == "" || strings.Contains(rest, "/") {
			writeError(w, http.StatusNotFound, "unknown VM sub-resource")
			return
		}
		vm := store.GetVMByUUID(rest)
		if vm == nil {
			writeError(w, http.StatusNotFound, "vm not found")
			return
		}
		writeJSON(w, http.StatusOK, vm)
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

		// Request-ID substrate (v5.32.17+, Tier S #4).
		//
		//   1. Client may supply a request_id (X-Request-Id header or
		//      body field) to opt into idempotent retries. If absent,
		//      server mints one and echoes it on the response.
		//   2. Server records a "pending" entry in the command log
		//      BEFORE dispatching to the agent. Log is durable — any
		//      crash mid-flight leaves a pending row the boot
		//      reconciler can drive to terminal state.
		//   3. Agent's outcome cache (keyed on request_id) returns
		//      the cached reply on replay, so a dropped response +
		//      client retry doesn't double-execute the command.
		if req.RequestID == "" {
			if h := r.Header.Get("X-Request-Id"); h != "" {
				req.RequestID = h
			} else {
				req.RequestID = NewRequestID()
			}
		}
		w.Header().Set("X-Request-Id", req.RequestID)
		// Audit every command that reaches this handler, regardless of
		// outcome. We re-audit with success=false in the error paths
		// below; RBAC/feature rejections are covered in the overarching
		// middleware which already 401/403s before we get here.
		defer func() {
			// Detail tag records which agent was called and which action
			// verb; lifecycle/snapshot ops all funnel through /api/command
			// so the action column alone isn't specific enough.
			detail := map[string]string{"action": action, "host_id": req.HostID}
			resource := "vm/" + req.VMUUID
			switch action {
			case "START", "STOP", "FORCESTOP", "RESTART", "POWER_CYCLE":
				audit(store, r, AuditVMAction, resource, detail, true, "")
			case "SNAPSHOT":
				audit(store, r, AuditVMSnapshot, resource, detail, true, "")
			case "CHANGE_MEDIA":
				for k, v := range req.Params {
					if k == "media_action" || k == "target" || k == "path" {
						detail[k] = v
					}
				}
				audit(store, r, AuditVMAction, resource, detail, true, "")
			case "EDIT_VM":
				for k, v := range req.Params {
					if k == "vcpus" || k == "mem_mb" {
						detail[k] = v
					}
				}
				audit(store, r, AuditVMAction, resource, detail, true, "")
			case "REVERT_SNAPSHOT":
				audit(store, r, AuditVMRevert, resource, detail, true, "")
			case "DELETE_SNAPSHOT":
				audit(store, r, AuditVMSnapDelete, resource, detail, true, "")
			case "MIGRATE", "OFFLINE_MIGRATE":
				// Carry the chosen transport into the audit detail so
				// the trail makes the SSH-vs-mTLS choice explicit.
				// Tier 2 #8, v5.37.1: `transport` is set from the
				// Migrate modal (auto / mtls / ssh); when SSH is used
				// the agent dials with `StrictHostKeyChecking=no`,
				// which is fine on a trusted management VLAN but
				// worth surfacing on the audit trail.
				if t := strings.TrimSpace(req.Params["transport"]); t != "" {
					detail["transport"] = t
				}
				if action == "OFFLINE_MIGRATE" {
					detail["mode"] = "offline"
				} else {
					detail["mode"] = "live"
				}
				audit(store, r, AuditMigrationStart, resource, detail, true, "")
			case "DELETE_VM":
				audit(store, r, AuditVMDelete, resource, detail, true, "")
			}
		}()
		var needPerm string
		switch action {
		case "START", "STOP", "FORCESTOP", "RESTART", "POWER_CYCLE":
			needPerm = models.PermVMLifecycle
		case "SNAPSHOT", "REVERT_SNAPSHOT", "DELETE_SNAPSHOT":
			needPerm = models.PermVMSnapshot
		case "MIGRATE", "OFFLINE_MIGRATE":
			needPerm = models.PermVMMigrate
		case "DELETE_VM":
			needPerm = models.PermVMDelete
		case "CHANGE_MEDIA", "EDIT_VM":
			// CDROM eject / insert + hardware edits (vcpus, memory) —
			// all covered under vm.edit (Operator role bundle).
			needPerm = models.PermVMEdit
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

		// v5.10.1 + v5.11.4: prefer qemu+tls:// (+ signed grants) when
		// both source and destination have an active hypervisor cert.
		// Applies to BOTH live (MIGRATE) and offline (OFFLINE_MIGRATE)
		// so the two paths share one authorisation story. Falls back
		// to legacy qemu+ssh when either side isn't enrolled yet — so
		// a mixed fleet during rollout keeps working.
		if action == "MIGRATE" || action == "OFFLINE_MIGRATE" {
			destHostID := resolveHostByIP(store, req.Params["destination"])
			if req.Params == nil {
				req.Params = map[string]string{}
			}
			// Auto-select unless the operator pinned a transport in
			// the UI (v5.11.4 toggle).
			if _, already := req.Params["migration_transport"]; !already {
				if destHostID != "" && store.HostMTLSReady(req.HostID) && store.HostMTLSReady(destHostID) {
					req.Params["migration_transport"] = "tls"
				} else {
					req.Params["migration_transport"] = "ssh"
				}
			}
			modeLabel := "live"
			if action == "OFFLINE_MIGRATE" {
				modeLabel = "offline"
			}
			if req.Params["migration_transport"] == "tls" {
				log.Printf("migration(%s): %s → %s using qemu+tls://", modeLabel, req.HostID, destHostID)
			}
			// Mint a signed grant whenever the transport is mTLS,
			// regardless of live/offline. Source agent forwards it
			// pre-migration; dest agent records it so the libvirt
			// hook can enforce.
			if req.Params["migration_transport"] == "tls" && serverGrantSigner != nil && destHostID != "" {
				envelope, grant, gerr := serverGrantSigner.Issue(req.HostID, destHostID, req.VMUUID, req.VMName, modeLabel)
				if gerr != nil {
					log.Printf("grants: issue failed for %s→%s vm=%s: %v", req.HostID, destHostID, req.VMName, gerr)
				} else {
					req.Params["grant"] = envelope
					auditSystem(store, AuditMigrationGrantIssue, "vm/"+req.VMUUID, map[string]string{
						"grant_id": grant.ID,
						"src":      grant.Source,
						"dst":      grant.Dest,
						"mode":     grant.Mode,
						"ttl":      fmt.Sprintf("%ds", grant.ExpiresAt-grant.IssuedAt),
					}, true, "")
				}
			}
		}
		agentCmd := models.CommandRequest{
			RequestID: req.RequestID,
			Action:    action,
			VMName:    req.VMName,
			VMUUID:    req.VMUUID,
			Params:    req.Params,
		}
		body, _ := json.Marshal(agentCmd)

		// Record the pending entry BEFORE the agent call so a server
		// crash mid-flight leaves a durable row for the boot
		// reconciler. If the same request_id was already seen, short-
		// circuit to the cached outcome — the caller is retrying a
		// command we already resolved.
		actorID := ""
		if u != nil {
			actorID = u.ID
		}
		existing, replay := store.commandLog.Record(&models.CommandLogEntry{
			RequestID:   req.RequestID,
			Action:      action,
			HostID:      req.HostID,
			VMUUID:      req.VMUUID,
			VMName:      req.VMName,
			Params:      req.Params,
			ActorID:     actorID,
			SubmittedAt: time.Now(),
			Outcome:     "pending",
		})
		if replay && existing.Outcome != "pending" {
			// Prior call already finished. Return the cached outcome
			// verbatim. Never re-drive the agent — that's the whole
			// point of the idempotency cache.
			log.Printf("command: replay from cache req_id=%s action=%s outcome=%s", req.RequestID, action, existing.Outcome)
			w.Header().Set("X-Command-Replayed", "true")
			w.Header().Set("Content-Type", "application/json")
			status := existing.StatusCode
			if status == 0 {
				status = http.StatusOK
			}
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(models.CommandResponse{
				RequestID: existing.RequestID,
				Replayed:  true,
				Success:   existing.Outcome == "success",
				Message:   existing.Message,
			})
			return
		}

		url := fmt.Sprintf("http://%s/command", agentAddr)
		log.Printf("command: %s → %s  vm=%s  req_id=%s", action, agentAddr, req.VMName, req.RequestID)

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
			destHost := resolveHostByIP(store, destIP)
			// Maintenance gate: refuse migrations INTO a host that's
			// draining for patch/reboot. The whole point of maintenance
			// mode is to move workload off the host, not onto it.
			if destHost != "" && store.IsHostInMaintenance(destHost) {
				writeError(w, http.StatusConflict,
					"destination host "+destHost+" is in maintenance mode — pick a different target")
				return
			}
			// Guard against piling up concurrent migrations on the same
			// source or destination host. The agent serialises offline
			// migrations internally (only one /migrate-status slot), and
			// live migrations stack but quickly thrash bandwidth + CPU.
			// Reject early with a clear message rather than letting the
			// agent fail mid-flight.
			for _, m := range store.GetMigrations() {
				if m.Status != "running" {
					continue
				}
				if m.SourceHost == req.HostID {
					writeError(w, http.StatusConflict,
						fmt.Sprintf("a migration is already in progress on source host %s (vm=%s, phase=%s) — wait for it to finish before starting another",
							req.HostID, m.VMName, m.Phase))
					return
				}
				if destHost != "" && m.DestHost == destHost {
					writeError(w, http.StatusConflict,
						fmt.Sprintf("a migration is already in progress targeting %s (vm=%s, phase=%s) — wait for it to finish before starting another",
							destHost, m.VMName, m.Phase))
					return
				}
			}
			// v5.11.4: both live and offline honour the server's auto-
			// selected transport (or the operator's pin). Legacy
			// behaviour was "offline always ssh" but offline now
			// participates in the grant workflow when both hosts are
			// mTLS-ready, so the record should reflect that.
			transport := req.Params["migration_transport"]
			if transport == "" {
				transport = "ssh"
			}
			migRec = &models.MigrationRecord{
				ID:         fmt.Sprintf("%s-%d", req.VMName, time.Now().UnixNano()),
				VMName:     req.VMName,
				SourceHost: req.HostID,
				DestHost:   destHost,
				Mode:       mode,
				Transport:  transport,
				Status:     "running",
				StartedAt:  time.Now().Unix(),
				Phase:      "starting",
				Progress:   0,
			}
			store.AddMigration(migRec)
			// v5.29.0: hand the agent the server-generated migration_id so
			// it can open a gRPC StreamMigrationProgress stream and push
			// phase/progress frames in real time. Without this the agent
			// has no way to correlate frames back to the server's record.
			agentCmd.Params["migration_id"] = migRec.ID
			body, _ = json.Marshal(agentCmd)
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
			store.commandLog.Update(req.RequestID, "failed", "agent unreachable: "+err.Error(), http.StatusBadGateway, false)
			writeError(w, http.StatusBadGateway, "agent unreachable: "+err.Error())
			return
		}
		defer resp.Body.Close()

		respBody, _ := io.ReadAll(resp.Body)
		// Resolve the command log outcome. Any 2xx is success; 4xx/5xx
		// is failed. Agent signals cache-replay via X-Command-Replayed
		// header; mirror onto the log + response for the audit trail.
		outcome := "success"
		if resp.StatusCode >= 400 {
			outcome = "failed"
		}
		var parsedResp struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(respBody, &parsedResp)
		msg := parsedResp.Message
		if msg == "" && resp.StatusCode >= 400 {
			msg = strings.TrimSpace(string(respBody))
		}
		replayed := resp.Header.Get("X-Command-Replayed") == "true"
		store.commandLog.Update(req.RequestID, outcome, msg, resp.StatusCode, replayed)
		if replayed {
			w.Header().Set("X-Command-Replayed", "true")
		}
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
		// v5.27.7: EDIT_VM + CHANGE_MEDIA return UpdatedFields in the
		// response body. Apply them to the in-memory VM record now so
		// the SPA's query-invalidation picks up fresh state instead of
		// waiting 30 s for the next telemetry tick.
		if resp.StatusCode < 400 && (action == "EDIT_VM" || action == "CHANGE_MEDIA") {
			var parsed struct {
				UpdatedFields map[string]any `json:"updated_fields"`
			}
			if err := json.Unmarshal(respBody, &parsed); err == nil && len(parsed.UpdatedFields) > 0 {
				store.ApplyVMFieldUpdates(req.HostID, req.VMUUID, req.VMName, parsed.UpdatedFields)
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

// finalizeMigration stamps a record as complete or failed and
// re-publishes it through the store so SSE clients see the terminal
// state. The field mutations + slice upsert run in one critical
// section inside Store.FinalizeMigration — no torn-read window
// (Tier 1 P0.2, v5.36).
func finalizeMigration(store *Store, rec *models.MigrationRecord, status, detail string) {
	store.FinalizeMigration(rec, status, detail)
	// Terminal migration → force a synchronous Save so a crash in
	// the next 30 s doesn't lose the terminal flip. The 30 s save
	// ticker would eventually catch it, but migration terminals are
	// exactly the records operators page on — worth paying a blocking
	// Mongo round-trip here. Tier S #3, v5.32.19.
	store.Save()
}

// pollMigrationStatus polls the source agent's /migrate-status endpoint
// every second and mirrors phase/progress/detail onto the in-store
// migrationPollerClaims is the set of migration IDs currently being
// polled. A fresh handleCommand goroutine and a boot-reconciler
// retry could otherwise race — each writing to the same
// MigrationRecord, each calling finalizeMigration, firing duplicate
// terminal audit events. Tier S #6, v5.32.18.
var migrationPollerClaims struct {
	mu sync.Mutex
	m  map[string]struct{}
}

func claimMigrationPoller(id string) bool {
	migrationPollerClaims.mu.Lock()
	defer migrationPollerClaims.mu.Unlock()
	if migrationPollerClaims.m == nil {
		migrationPollerClaims.m = make(map[string]struct{})
	}
	if _, ok := migrationPollerClaims.m[id]; ok {
		return false
	}
	migrationPollerClaims.m[id] = struct{}{}
	return true
}

func releaseMigrationPoller(id string) {
	migrationPollerClaims.mu.Lock()
	defer migrationPollerClaims.mu.Unlock()
	delete(migrationPollerClaims.m, id)
}

// migration record. Stops when the agent reports the migration is no
// longer Active, or after a 90-minute safety net.
func pollMigrationStatus(store *Store, rec *models.MigrationRecord, agentAddr string) {
	if !claimMigrationPoller(rec.ID) {
		log.Printf("migration-poller: already polling id=%s vm=%s — second claim refused", rec.ID, rec.VMName)
		return
	}
	defer releaseMigrationPoller(rec.ID)
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
		store.UpdateMigrationProgress(rec, s.Phase, s.Progress, s.Detail)
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
		audit(store, r, AuditVMNotes, "vm/"+req.UUID, map[string]string{"chars": strconv.Itoa(len(req.Notes))}, true, "")
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
		originalUUID := p.VMUUID
		canonical := store.ApplyGuestTelemetry(&p)
		log.Printf("guest-telemetry: uuid=%s host=%s os=%q kvm-tools=%s", canonical, p.Hostname, p.OS, p.KVMToolsVersion)
		// If the operator queued a kvm-tools self-update for this VM,
		// hand back the binary URL so the in-guest daemon downloads it
		// and re-execs. The queue clears immediately.
		reply := map[string]string{"status": "ok"}
		if store.ConsumeKVMToolsUpdate(canonical) {
			reply["update_url"] = kvmToolsBinaryURL(r, p.OS)
			log.Printf("kvm-tools-update: signalled %s (os=%q) → %s", canonical, p.OS, reply["update_url"])
		}
		// Echo the canonical UUID when it differs from what the guest
		// claimed — the client caches this to local disk and pins it
		// for subsequent reports, fixing cloned-VM DMI UUID collisions
		// without operator intervention.
		if !strings.EqualFold(canonical, originalUUID) {
			reply["canonical_uuid"] = canonical
		}
		writeJSON(w, http.StatusOK, reply)
	}
}

// kvmToolsBinaryURL returns the URL the in-guest daemon should pull
// its self-update from. Picks the right binary based on the guest
// OS string carried in the telemetry payload:
//
//   - Any OS string containing "windows" → kvm-tools-windows-amd64.exe
//   - everything else → kvm-tools-linux-amd64
//
// We default to linux/amd64 because that's >95% of the fleet; arm64
// guests still get the amd64 binary (harmless — arm64 Linux guests
// have binfmt_misc or qemu-user configured by default on most
// distros, but this is a known limitation). A future
// GuestTelemetryPayload extension will ship goos+goarch explicitly
// so we can pick precisely. Tier S item, tracked in the Windows
// kvm-tools update bug memory.
//
// Since v5.16 downloads live on the server itself under
// /downloads/*, so the URL is same-origin with whatever port the
// guest originally registered against.
func kvmToolsBinaryURL(r *http.Request, guestOS string) string {
	if env := os.Getenv("KVM_TOOLS_BINARY_URL"); env != "" {
		return env
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	binary := "kvm-tools-linux-amd64"
	if strings.Contains(strings.ToLower(guestOS), "windows") {
		binary = "kvm-tools-windows-amd64.exe"
	}
	return fmt.Sprintf("%s://%s/downloads/%s", scheme, r.Host, binary)
}

// GET /api/kvm-tools/version — returns the kvm-tools build the server
// is shipping (== the committed VERSION). UI uses this to mark guest
// daemons as "outdated".
func handleKVMToolsVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"version": Version,
		// Empty guestOS → defaults to linux/amd64, which is what
		// the SPA uses for the "Linux install one-liner". Windows
		// guests hit the OS-aware path during self-update instead.
		"binary_url": kvmToolsBinaryURL(r, ""),
	})
}

// POST /api/kvm-tools/update — accepts {"uuids": [...]} and queues each
// VM for a self-update on its next telemetry POST. Idempotent.
func handleKVMToolsUpdate(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var req struct {
			UUIDs []string `json:"uuids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if len(req.UUIDs) == 0 {
			writeError(w, http.StatusBadRequest, "uuids must be non-empty")
			return
		}
		store.QueueKVMToolsUpdate(req.UUIDs)
		audit(store, r, AuditKVMToolsQueue, "kvm-tools", map[string]string{"uuids": strings.Join(req.UUIDs, ",")}, true, "")
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "queued",
			"count":  len(req.UUIDs),
		})
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
		// Maintenance gate: reject new-VM placement on draining hosts.
		// Mirror of the block in handleCommand's migration path.
		if store.IsHostInMaintenance(hostID) {
			writeError(w, http.StatusConflict,
				"host "+hostID+" is in maintenance mode — choose a different target")
			return
		}
		agentAddr := store.GetHostAgentAddr(hostID)
		if agentAddr == "" {
			writeError(w, http.StatusNotFound, "host not found")
			return
		}
		// Buffer the body so we can re-use it for the agent POST while
		// still being able to peek at the VM name for the audit event.
		bodyBytes, _ := io.ReadAll(r.Body)
		var meta struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(bodyBytes, &meta)

		client := &http.Client{Timeout: 5 * time.Minute}
		resp, err := client.Post(
			fmt.Sprintf("http://%s/vm-create", agentAddr),
			"application/json", bytes.NewReader(bodyBytes))
		if err != nil {
			writeError(w, http.StatusBadGateway, "agent unreachable: "+err.Error())
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		// Audit the create attempt either way — failures are
		// operationally important too. Resource is "vm/<name>" since
		// we don't know the libvirt UUID until after the agent replies.
		ok := resp.StatusCode < 400
		errMsg := ""
		if !ok {
			errMsg = strings.TrimSpace(string(body))
		}
		audit(store, r, AuditVMCreate, "vm/"+meta.Name, map[string]string{"host_id": hostID}, ok, errMsg)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(body)
	}
}

// POST /api/migrations/cancel?id=<migration-id> — ask the source agent to
// abort the in-flight migration. For offline migrations the agent's
// goroutine observes the cancelled context, breaks out of its current
// step, and falls through rollback() which restores the source VM. For
// live migrations we send `virsh domjobabort` to the source as a best-
// effort cancel — virsh aborts the in-flight migration and resumes
// execution on the source. Once the cleanup phase has begun the source
// disks are gone; we refuse to cancel past that point.
func handleCancelMigration(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		id := r.URL.Query().Get("id")
		if id == "" {
			writeError(w, http.StatusBadRequest, "id query param required")
			return
		}
		var rec *models.MigrationRecord
		for _, m := range store.GetMigrations() {
			if m.ID == id {
				rec = m
				break
			}
		}
		if rec == nil {
			writeError(w, http.StatusNotFound, "migration not found")
			return
		}
		if rec.Status != "running" {
			writeError(w, http.StatusConflict, "migration is not running (status="+rec.Status+")")
			return
		}
		if rec.Phase == "cleanup" {
			writeError(w, http.StatusConflict,
				"too late to cancel — source disks have already been removed during the cleanup phase")
			return
		}
		agentAddr := store.GetHostAgentAddr(rec.SourceHost)
		if agentAddr == "" {
			writeError(w, http.StatusNotFound, "source agent unreachable")
			return
		}
		// Update record so the SSE clients see the cancelling state
		// immediately — the poller will overwrite once the rollback
		// completes (status → failed, error → "cancelled by operator").
		rec.Phase = "cancelling"
		rec.Detail = "Operator requested cancel — rolling back…"
		store.AddMigration(rec)

		audit(store, r, AuditMigrationCancel, "migration/"+id, map[string]string{"vm": rec.VMName, "phase": rec.Phase}, true, "")
		url := fmt.Sprintf("http://%s/cancel-migrate?vm=%s", agentAddr, rec.VMName)
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Post(url, "application/json", nil)
		if err != nil {
			writeError(w, http.StatusBadGateway, "agent unreachable: "+err.Error())
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		// For LIVE migrations the agent's offline migration tracker is
		// empty (offlineMigrate isn't running) so the agent returns 404
		// + the best-effort domjobabort path. Either way, finalize the
		// record as failed/cancelled here so the UI doesn't hang on
		// "cancelling" forever for live migrations.
		if rec.Mode == "live" {
			finalizeMigration(store, rec, "failed", "cancelled by operator (virsh domjobabort)")
		}
		log.Printf("cancel-migration: %s phase=%s mode=%s — agent: %s", rec.VMName, rec.Phase, rec.Mode, strings.TrimSpace(string(body)))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	}
}

// POST /api/migrations/force-fail?id=<migration-id> — admin escape
// hatch for a migration record stranded in "running" / "cancelling"
// that the normal cancel flow refuses to touch (e.g. agent crashed
// mid-cleanup). Marks the record "failed" with an explicit
// "manually failed" message, fires the standard terminal
// notification, and writes an audit event. Does NOT talk to the
// agent — the caller has presumably already confirmed the source/
// destination VM state out-of-band. Tier S #12, v5.32.18.
func handleForceFailMigration(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		id := r.URL.Query().Get("id")
		if id == "" {
			writeError(w, http.StatusBadRequest, "id query param required")
			return
		}
		reason := r.URL.Query().Get("reason")
		if reason == "" {
			reason = "manually failed by operator"
		}
		var rec *models.MigrationRecord
		for _, m := range store.GetMigrations() {
			if m.ID == id {
				rec = m
				break
			}
		}
		if rec == nil {
			writeError(w, http.StatusNotFound, "migration not found")
			return
		}
		if isTerminalMigration(rec.Status) {
			writeError(w, http.StatusConflict, "migration already terminal (status="+rec.Status+")")
			return
		}
		audit(store, r, AuditMigrationCancel, "migration/"+id, map[string]string{
			"vm":     rec.VMName,
			"phase":  rec.Phase,
			"reason": reason,
			"forced": "true",
		}, true, "")
		log.Printf("migration force-fail: id=%s vm=%s phase=%s reason=%q", id, rec.VMName, rec.Phase, reason)
		finalizeMigration(store, rec, "failed", reason)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id, "status": "failed"})
	}
}

// GET /api/vms/screenshot?host=<id>&vm=<name> — proxy the agent's
// screenshot PNG. Exists because the browser renders the kvm-center UI
// over HTTPS (:8190) since v5.0 and can't load a plain-HTTP <img src>
// pointing directly at the agent's :8081 endpoint (mixed-content
// block). Routing the bytes through the server keeps everything on the
// TLS origin while the hypervisor agent stays plain-HTTP on the LAN.
func handleVMScreenshot(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// v5.18.7: screenshot thumbnails are part of the console surface;
		// honour disable_console so an operator who flipped the flag
		// doesn't still leak live VM framebuffers over /api/vms/screenshot.
		if featureBlocked(w, store, "console", "VM console access") {
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
			writeError(w, http.StatusNotFound, "host not found")
			return
		}
		upstream := fmt.Sprintf("http://%s/screenshot/%s", agentAddr, url.PathEscape(vmName))
		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Get(upstream)
		if err != nil {
			writeError(w, http.StatusBadGateway, "agent unreachable")
			return
		}
		defer resp.Body.Close()
		// Passthrough content type + cache hints so the browser treats
		// this identically to the direct-from-agent PNG it used to get.
		for _, h := range []string{"Content-Type", "Content-Length", "Cache-Control", "ETag", "Last-Modified"} {
			if v := resp.Header.Get(h); v != "" {
				w.Header().Set(h, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
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
