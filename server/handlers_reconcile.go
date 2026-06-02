package main

// Virsh reconciliation (Tier S #1, v5.32.21).
//
// Telemetry already replaces the server's VM inventory wholesale on
// every agent tick (every ~15 s), so routine drift corrects itself
// quickly. This endpoint lets an admin trigger an *immediate* refresh
// and see a concrete diff — useful when:
//   - An operator just ran `virsh define` / `virsh undefine` on a host
//     and wants to confirm the SPA reflects it without waiting a tick.
//   - A host was briefly offline and the operator wants to verify the
//     current cache matches virsh now that the agent is back.
//   - Post-incident triage: "is what I'm seeing fresh, or cached?"
//
// Flow:
//   1. Snapshot the server's current VM list + LastSeen for the host.
//   2. POST to the agent's /refresh-telemetry endpoint.
//   3. Agent runs one collection + send synchronously, returns 200.
//   4. Server's /api/telemetry handler has updated state by the time
//      the agent's reply arrives (same goroutine chain).
//   5. Build a diff report: VMs present only in old, only in new,
//      and VMs whose state changed.

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"kvm-center/internal/models"
)

type reconcileReport struct {
	HostID           string      `json:"host_id"`
	RefreshedAt      time.Time   `json:"refreshed_at"`
	PreviousLastSeen time.Time   `json:"previous_last_seen"`
	NewLastSeen      time.Time   `json:"new_last_seen"`
	AddedVMs         []vmBrief   `json:"added_vms"`
	RemovedVMs       []vmBrief   `json:"removed_vms"`
	StateChanged     []vmChanged `json:"state_changed"`
	PreviousCount    int         `json:"previous_count"`
	NewCount         int         `json:"new_count"`
	AgentStatus      string      `json:"agent_status"` // "refreshed" | "unreachable" | "skipped"
	AgentError       string      `json:"agent_error,omitempty"`
}

type vmBrief struct {
	UUID  string         `json:"uuid"`
	Name  string         `json:"name"`
	State models.VMState `json:"state"`
}

type vmChanged struct {
	UUID string         `json:"uuid"`
	Name string         `json:"name"`
	From models.VMState `json:"from"`
	To   models.VMState `json:"to"`
}

func handleReconcileHost(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		hostID := strings.TrimPrefix(r.URL.Path, "/api/admin/reconcile/")
		hostID = strings.TrimSuffix(hostID, "/")
		if hostID == "" {
			writeError(w, http.StatusBadRequest, "host id required in path")
			return
		}
		host := store.GetHost(hostID)
		if host == nil {
			writeError(w, http.StatusNotFound, "host not found")
			return
		}

		// Snapshot pre-refresh state.
		prevVMs := briefsFromStore(store, hostID)
		prevLastSeen := host.LastSeen

		rep := reconcileReport{
			HostID:           hostID,
			RefreshedAt:      time.Now().UTC(),
			PreviousLastSeen: prevLastSeen,
			PreviousCount:    len(prevVMs),
			AddedVMs:         []vmBrief{},
			RemovedVMs:       []vmBrief{},
			StateChanged:     []vmChanged{},
		}

		// Ping the agent. Unreachable hosts return a partial report
		// (no diff, but the snapshot fields tell the operator why).
		agentAddr := store.GetHostAgentAddr(hostID)
		if agentAddr == "" {
			rep.AgentStatus = "skipped"
			rep.AgentError = "no agent address on record"
			writeJSON(w, http.StatusOK, rep)
			return
		}
		client := store.newAgentClient(hostID, 10*time.Second)
		url := fmt.Sprintf("http://%s/refresh-telemetry", agentAddr)
		resp, err := client.Post(url, "application/json", nil)
		if err != nil {
			rep.AgentStatus = "unreachable"
			rep.AgentError = err.Error()
			writeJSON(w, http.StatusOK, rep)
			return
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			rep.AgentStatus = "unreachable"
			rep.AgentError = fmt.Sprintf("agent returned %d", resp.StatusCode)
			writeJSON(w, http.StatusOK, rep)
			return
		}
		rep.AgentStatus = "refreshed"

		// Post-refresh snapshot. The agent's refresh-telemetry call
		// ran synchronously and POSTed /api/telemetry, which
		// ApplyTelemetry processed under s.mu before returning. By
		// the time we read here, state is fresh.
		newVMs := briefsFromStore(store, hostID)
		newHost := store.GetHost(hostID)
		if newHost != nil {
			rep.NewLastSeen = newHost.LastSeen
		}
		rep.NewCount = len(newVMs)

		// Diff by UUID (case-insensitive). Any VM present in one but
		// not the other is added/removed; present in both but with a
		// different state is recorded under StateChanged.
		prevByUUID := indexByUUID(prevVMs)
		newByUUID := indexByUUID(newVMs)
		for uuid, v := range newByUUID {
			if old, ok := prevByUUID[uuid]; !ok {
				rep.AddedVMs = append(rep.AddedVMs, v)
			} else if old.State != v.State {
				rep.StateChanged = append(rep.StateChanged, vmChanged{
					UUID: v.UUID, Name: v.Name, From: old.State, To: v.State,
				})
			}
		}
		for uuid, v := range prevByUUID {
			if _, ok := newByUUID[uuid]; !ok {
				rep.RemovedVMs = append(rep.RemovedVMs, v)
			}
		}

		audit(store, r, "host.reconcile", "host/"+hostID, map[string]string{
			"added":   fmt.Sprintf("%d", len(rep.AddedVMs)),
			"removed": fmt.Sprintf("%d", len(rep.RemovedVMs)),
			"changed": fmt.Sprintf("%d", len(rep.StateChanged)),
		}, true, "")
		writeJSON(w, http.StatusOK, rep)
	}
}

func briefsFromStore(store *Store, hostID string) []vmBrief {
	vms := store.GetVMs(hostID)
	out := make([]vmBrief, 0, len(vms))
	for _, v := range vms {
		if v == nil {
			continue
		}
		out = append(out, vmBrief{UUID: v.UUID, Name: v.Name, State: v.State})
	}
	return out
}

func indexByUUID(vms []vmBrief) map[string]vmBrief {
	out := make(map[string]vmBrief, len(vms))
	for _, v := range vms {
		key := strings.ToLower(v.UUID)
		if key == "" {
			key = strings.ToLower(v.Name) // fallback for VMs without UUID
		}
		out[key] = v
	}
	return out
}
