package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
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

		// Always derive agent IP from the connection — the payload's
		// self-reported IP is unauthenticated and could be spoofed to
		// poison inventory entries.
		payload.IP = clientIP(r)
		if payload.Timestamp.IsZero() {
			payload.Timestamp = time.Now()
		}
		if payload.AgentPort == 0 {
			payload.AgentPort = 8081
		}

		// v5.54.0 — reject telemetry from a retired host_id. Without
		// this, ApplyTelemetry would silently re-create the host row
		// every 15 s, undoing the operator's Delete click. Status 410
		// (Gone) is the right shape; agents that get this should stop
		// telemetering. The retire flow already SHOULD have fired
		// RETIRE at the agent — this is the belt-and-braces guard for
		// agents that were unreachable at retire time.
		if store.IsHostRemoved(payload.HostID) {
			writeError(w, http.StatusGone, "host has been retired from this kvm-center; uninstall the agent or unretire to re-enrol")
			return
		}

		store.ApplyTelemetry(&payload)
		// Log once every 5 minutes per host instead of every tick, so
		// large fleets don't drown the log shipper. The audit log
		// captures the per-tick state machine; this log line is just
		// for human-eyeballing.
		if shouldLogTelemetry(payload.HostID) {
			log.Printf("telemetry: %s  host=%s  vms=%d  cpu=%.1f%%  mem=%.1f%%",
				payload.IP, payload.HostID, len(payload.VMs), payload.CPUUsage, payload.MemUsage)
		}

		// Gossip the shared console-token secret so agents can verify
		// signatures minted by /api/vms/console-token. Ships on every
		// reply so a rotating secret (future feature) propagates
		// automatically within one telemetry tick.
		writeJSON(w, http.StatusOK, map[string]any{
			"status":         "ok",
			"console_secret": store.GetOrCreateConsoleSecret(),
			// command_auth signals that this server signs its command
			// requests (C1, internal/agentauth). Agents begin enforcing
			// signatures only once they see this true, so an old/unsigning
			// server can't lock the fleet out. Honors the kill-switch.
			"command_auth": !commandAuthDisabled(),
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

		// v5.54.0 retire flow:
		//   1. fire RETIRE at the agent so it stops + cleans up files
		//   2. add host_id to the deny-list so a straggling agent
		//      can't recreate the row via /api/telemetry
		//   3. then DeleteHost as before
		// Query string ?keep_files=1 leaves /opt/kvm-center, certs, and
		// libvirt drop-ins alone (only stops the systemd unit). Default
		// is full uninstall — matches what an operator clicking Delete
		// usually wants.
		keepFiles := r.URL.Query().Get("keep_files") == "1"
		reason := strings.TrimSpace(r.URL.Query().Get("reason"))
		agentRetireErr := sendAgentRetire(store, h, !keepFiles)
		if agentRetireErr != nil {
			log.Printf("retire: agent unreachable for %s — proceeding with deny-list anyway: %v", id, agentRetireErr)
		}

		actor := ""
		if u := userFromRequest(r); u != nil {
			actor = u.ID
		}
		store.MarkHostRemoved(id, actor, reason)
		store.DeleteHost(id)

		auditDetail := map[string]string{
			"vm_rows":      strconv.Itoa(vmCount),
			"storage_rows": strconv.Itoa(poolCount),
			"reason":       reason,
			"keep_files":   boolToStr(keepFiles),
		}
		if agentRetireErr != nil {
			auditDetail["agent_retire_error"] = agentRetireErr.Error()
		}
		audit(store, r, AuditHostDelete, "host/"+id, auditDetail, true, "")
		// v5.60.10 — also surface in Recent Tasks. Host retire is a
		// high-visibility operator action; pre-fix it only landed in
		// the audit log, which surprised operators expecting the pane
		// to show "what I just did". See feedback_recent_tasks_scope.md.
		recordOperatorTask(store, r, AuditHostDelete, "host/"+id, auditDetail, true, "")
		writeJSON(w, http.StatusOK, map[string]any{
			"deleted":            id,
			"vm_rows":            vmCount,
			"storage_rows":       poolCount,
			"agent_retired":      agentRetireErr == nil,
			"agent_retire_error": agentRetireErrToStr(agentRetireErr),
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

		// RBAC check: the caller's role must grant the matching
		// permission. v5.60.0 — also enforce token-scope narrowing
		// here, matching what requirePerm does for routes with
		// statically-known perms. Pre-v5.60.0 this check only
		// consulted hasPermission(user, perm) and ignored token
		// scopes, so a viewer-scoped token from an admin user could
		// still drive lifecycle actions through /api/command.
		// Surfaced by e2e round 3 RBAC scenario U4.
		u := userFromRequest(r)
		if u == nil {
			writeError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		if scopes := tokenScopesFromRequest(r); len(scopes) > 0 {
			if !scopeAllows(scopes, needPerm) {
				writeError(w, http.StatusForbidden, "token scope does not include: "+needPerm)
				return
			}
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
			// v5.44.0: thread the operator's StrictHostKeyChecking
			// preference through to the agent. The agent reads the
			// param at dispatch time (action_migrate.go calls
			// setSSHStrict) so the SSH-transport path picks up the
			// right `-o StrictHostKeyChecking=yes/no`.
			if store.GetFeatures().SSHStrictHostKeyChecking {
				req.Params["strict_host_key"] = "yes"
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
		// v5.48.1 (PR-3c): refuse a second mutating action on the
		// same VM while one is still in flight. Same request_id
		// (idempotent retry) is allowed through to the Record call
		// below where the cache short-circuits replay; only a
		// genuinely-different request_id targeting the same VM
		// triggers 409 Conflict. Skip when both UUID and name are
		// empty (some commands operate fleet-wide rather than on a
		// specific VM and shouldn't gate each other).
		if conflict := findInflightConflict(store.commandLog, action, req.VMUUID, req.VMName, req.RequestID); conflict != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":                  "another action is already in flight for this target",
				"conflicting_request_id": conflict.RequestID,
				"conflicting_action":     conflict.Action,
				"submitted_at":           conflict.SubmittedAt,
			})
			return
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
				RequestID:  req.RequestID,
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
		// EDIT_VM with allow_shutdown=true runs a shutdown→edit→start
		// cycle bounded by libvirt's shutdown grace window — up to a
		// few minutes on slow / emulated qemu hosts. v5.55.5 lifted
		// from the default 15 s to 60 s but real-world emulated runs
		// still exceeded it (proved out on hostD dogfood); v5.55.6
		// lifts to 10 min so the operator gets the agent's actual
		// outcome instead of a misleading 502 ghost-failure.
		timeout := 15 * time.Second
		switch action {
		case "MIGRATE", "OFFLINE_MIGRATE":
			timeout = 30 * time.Minute
		case "EDIT_VM":
			if strings.EqualFold(req.Params["allow_shutdown"], "true") {
				timeout = 10 * time.Minute
			}
		}
		client := store.newAgentClient(req.HostID, timeout)
		resp, err := client.Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			// Transport / deadline failures don't tell us whether the
			// agent applied the command — we just lost the response.
			// Migration records still flip to failed (their separate
			// /migrate-status feed is the authoritative signal), but
			// the command-log entry stays "pending" with InflightUntil
			// set so the v5.47.2+ reconciler can resolve it from the
			// agent's outcome cache. Pre-5.47.2 agents respond 404 on
			// /command-outcome → we fall back to the existing 45-min
			// stranded sweep.
			if migRec != nil {
				finalizeMigration(store, migRec, "failed", "agent unreachable: "+err.Error())
			}
			deadline := time.Now().Add(outcomeReconcileWindow)
			store.commandLog.MarkPending(req.RequestID, deadline, "agent call timed out: "+err.Error()+"; outcome will be reconciled")
			writeError(w, http.StatusBadGateway, "agent unreachable: "+err.Error())
			return
		}
		defer resp.Body.Close()

		respBody, _ := io.ReadAll(resp.Body)
		// Resolve the command log outcome. 2xx → success. 4xx →
		// failed (agent intentionally rejected the request). 5xx →
		// pending: agent had an internal error, which doesn't tell us
		// whether the underlying libvirt operation actually applied,
		// so we let the reconciler resolve it the same way as a
		// transport error. Agent signals cache-replay via
		// X-Command-Replayed header; mirror onto the log + response
		// for the audit trail.
		var parsedResp struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(respBody, &parsedResp)
		msg := parsedResp.Message
		if msg == "" && resp.StatusCode >= 400 {
			msg = strings.TrimSpace(string(respBody))
		}
		replayed := resp.Header.Get("X-Command-Replayed") == "true"
		// v5.55.5: peek at the body's explicit `success` flag — when
		// the agent returns a parseable response declaring success=false
		// (e.g. "Domain is already active", "DELETE_SNAPSHOT requires
		// params.name"), treat as terminal `failed` regardless of HTTP
		// status. Without this, the agent's `respondWithFlags` returning
		// 500-with-body-success-false used to send the entry to
		// MarkPending with InflightUntil=now+30m, locking the VM for
		// every other action behind a 409 conflict while the reconciler
		// caught up. The body has the answer — use it.
		var bodySuccess struct {
			Success *bool `json:"success"`
		}
		hasExplicitOutcome := json.Unmarshal(respBody, &bodySuccess) == nil && bodySuccess.Success != nil
		switch {
		case resp.StatusCode == http.StatusAccepted:
			// v5.48 PR-2e: agent accepted the action and will run
			// it asynchronously. Server treats this as pending; the
			// PR-1c reconciler polls /command-outcome to recover
			// the terminal state once the agent completes.
			deadline := time.Now().Add(outcomeReconcileWindow)
			store.commandLog.MarkPending(req.RequestID, deadline, "agent accepted async (202); outcome will be reconciled")
		case hasExplicitOutcome && !*bodySuccess.Success:
			// Deterministic application error — agent has a body and
			// has declared the operation failed. Terminal.
			store.commandLog.Update(req.RequestID, "failed", msg, resp.StatusCode, replayed)
			notifyTaskOutcome(store, existing, "failed", msg)
		case hasExplicitOutcome && *bodySuccess.Success:
			// Body says success=true. Trust it.
			store.commandLog.Update(req.RequestID, "success", msg, resp.StatusCode, replayed)
			notifyTaskOutcome(store, existing, "success", msg)
		case resp.StatusCode >= 500:
			// 5xx without an explicit body outcome: genuine transport /
			// internal failure where the agent didn't get a chance to
			// frame its answer. Keep pending and let the reconciler
			// query /command-outcome.
			deadline := time.Now().Add(outcomeReconcileWindow)
			store.commandLog.MarkPending(req.RequestID, deadline, "agent returned "+strconv.Itoa(resp.StatusCode)+": "+msg+"; outcome will be reconciled")
		case resp.StatusCode >= 400:
			store.commandLog.Update(req.RequestID, "failed", msg, resp.StatusCode, replayed)
			notifyTaskOutcome(store, existing, "failed", msg)
		default:
			store.commandLog.Update(req.RequestID, "success", msg, resp.StatusCode, replayed)
			notifyTaskOutcome(store, existing, "success", msg)
		}
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
//
// v5.50.3+: also resolves the matching command-log entry (looked up
// by MigrationRecord.RequestID) so the VM's per-target lock releases
// the moment the migration's terminal state is recorded. Without this,
// a migration that finishes in 154 s could keep the VM 409-locked
// for the full 45-min `pendingReconcileAge` window, blocking unrelated
// operations like snapshots.
func finalizeMigration(store *Store, rec *models.MigrationRecord, status, detail string) {
	store.FinalizeMigration(rec, status, detail)
	// Mirror the migration's terminal status onto the command-log entry
	// that started it. Map "complete" → "success", "failed" → "failed",
	// "cancelled" → "cancelled". No-op when the record has no RequestID
	// (internal pathway or pre-v5.50.3 record) or the command-log entry
	// has already been resolved by some other path (Update is a no-op
	// when the entry is already terminal).
	if rec.RequestID != "" && store.commandLog != nil {
		clOutcome := status
		if status == "complete" {
			clOutcome = "success"
		}
		clMsg := detail
		if clMsg == "" {
			clMsg = "resolved by finalizeMigration: " + status
		}
		store.commandLog.Update(rec.RequestID, clOutcome, clMsg, 0, false)
	}
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
	// v5.60.18 — honour the process shutdown context so SIGTERM
	// during a long migration doesn't leave a 90-min goroutine
	// looping against a half-torn-down store. Use serverCtx (set
	// in main.go) so the poller terminates cleanly when the
	// server is told to stop. Pre-fix the goroutine could outlive
	// the HTTP server and call finalizeMigration → Save() against
	// a store whose backend may already be closed.
	ctx := serverCtx
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(90 * time.Minute)
	client := store.newAgentClient(rec.SourceHost, 4*time.Second)
	url := fmt.Sprintf("http://%s/migrate-status", agentAddr)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			log.Printf("migration-poller: context cancelled mid-poll id=%s vm=%s", rec.ID, rec.VMName)
			return
		case <-time.After(1 * time.Second):
		}
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
		client := store.newAgentClient(hostID, 10*time.Second)
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
		client := store.newAgentClient(hostID, 10*time.Second)
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
		client := store.newAgentClient(hostID, 30*time.Minute)
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
		client := store.newAgentClient(hostID, 15*time.Second)
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
		client := store.newAgentClient(hostID, 15*time.Second)
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

		client := store.newAgentClient(hostID, 5*time.Minute)
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
		client := store.newAgentClient(rec.SourceHost, 10*time.Second)
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
		client := store.newAgentClient(hostID, 15*time.Second)
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
		client := store.newAgentClient(hostID, 5*time.Second)
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

// GET /health — liveness probe with subsystem depth.
//
// Pre-v5.60.15 this answered HTTP 200 + {"status":"ok"} on every
// request — even when the Mongo backend was unreachable, certs were
// minutes from expiry, or every agent had gone silent. External
// monitors (k8s liveness, uptime checks, Discord ops bot) polling
// /health learned nothing.
//
// v5.60.15 → v5.60.19 contract (post-back-compat fix):
//
//   - HTTP 200 when serving (overall green or amber). Tools that
//     historically grepped {"status":"ok"} keep working — the
//     field is still "ok" whenever the server is up and answering.
//   - HTTP 503 when overall=red (Mongo unreachable, cert critical,
//     etc.). Body's `status` becomes "down". Container orchestrators
//     and uptime probes notice without needing the admin-gated
//     /api/system-health route.
//   - The depth signal lives in the new `overall` field
//     (green | amber | red), so a tool that wants per-subsystem
//     state can read it without an extra round-trip.
func handleHealth(store *Store, ca *ClusterCA, reloader *CertReloader, signer *GrantSigner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		overall := "green"
		h := getCachedSystemHealth(store, ca, reloader, signer)
		if h != nil && h.Overall != "" {
			overall = h.Overall
		}
		status := http.StatusOK
		bodyStatus := "ok"
		if overall == "red" {
			status = http.StatusServiceUnavailable
			bodyStatus = "down"
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, status, map[string]string{
			"status":  bodyStatus,
			"overall": overall,
			"version": Version,
		})
	}
}

// clientIP extracts the real client IP, honouring X-Forwarded-For.
// telemetryLogState tracks the last log time per host so we don't spam
// the log file once a tick per agent — that scales linearly with fleet
// size and crowds out actual signal. 5 min cadence per host gives
// enough resolution to debug agent connectivity without a flood.
var (
	telemetryLogMu   sync.Mutex
	telemetryLogLast = map[string]time.Time{}
)

const telemetryLogInterval = 5 * time.Minute

func shouldLogTelemetry(hostID string) bool {
	telemetryLogMu.Lock()
	defer telemetryLogMu.Unlock()
	now := time.Now()
	if last, ok := telemetryLogLast[hostID]; ok && now.Sub(last) < telemetryLogInterval {
		return false
	}
	telemetryLogLast[hostID] = now
	return true
}

// trustedProxyCIDRs is the set of CIDRs whose RemoteAddr is allowed
// to set X-Forwarded-For on inbound requests. Populated at process
// start from KVM_CENTER_TRUSTED_PROXIES (comma-separated CIDR list).
// When empty (default), XFF is ignored and clientIP returns
// RemoteAddr only — safe-by-default behaviour for deployments that
// expose kvm-center directly to clients.
var trustedProxyCIDRs []*net.IPNet

func loadTrustedProxies() {
	raw := os.Getenv("KVM_CENTER_TRUSTED_PROXIES")
	if raw == "" {
		return
	}
	out := make([]*net.IPNet, 0, 4)
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// Allow bare IPs as a /32 (or /128) shorthand.
		if !strings.Contains(p, "/") {
			if strings.Contains(p, ":") {
				p += "/128"
			} else {
				p += "/32"
			}
		}
		_, cidr, err := net.ParseCIDR(p)
		if err != nil {
			log.Printf("KVM_CENTER_TRUSTED_PROXIES: ignoring invalid entry %q: %v", p, err)
			continue
		}
		out = append(out, cidr)
	}
	trustedProxyCIDRs = out
	if len(out) > 0 {
		log.Printf("trusted-proxies: %d CIDR(s) configured — XFF will be honoured for matching peers", len(out))
	}
}

// remoteAddrIP strips the port from RemoteAddr and trims IPv6 brackets.
func remoteAddrIP(remoteAddr string) string {
	ip := remoteAddr
	if idx := strings.LastIndex(ip, ":"); idx != -1 {
		ip = ip[:idx]
	}
	return strings.Trim(ip, "[]")
}

// clientIP returns the real client IP. X-Forwarded-For is honoured
// only when r.RemoteAddr is in the configured KVM_CENTER_TRUSTED_PROXIES
// allowlist; otherwise the header is ignored. This prevents an
// unauthenticated client from spoofing audit-log ActorIP or bypassing
// the per-IP login throttle simply by setting the header themselves.
func clientIP(r *http.Request) string {
	direct := remoteAddrIP(r.RemoteAddr)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if peerIP := net.ParseIP(direct); peerIP != nil {
			for _, cidr := range trustedProxyCIDRs {
				if cidr.Contains(peerIP) {
					return strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
				}
			}
		}
	}
	return direct
}
