package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"kvm-center/internal/models"
)

// Snapshot policies (v5.3). A background goroutine wakes once a minute
// and, for every enabled policy whose LastRunAt + EveryMinutes is in
// the past, kicks off a SNAPSHOT command per target VM via the same
// agent plumbing the UI "Create snapshot" button uses. After a
// successful create it queries the agent's snapshot list and deletes
// any policy-generated snapshots past RetentionKeep — identified by
// the naming prefix this file controls, so manual snapshots are
// never touched.

const snapshotNamePrefix = "policy-"

// snapshotName returns the name the scheduler uses for policy-
// generated snapshots. Format:
//
//	policy-<policyID>-<YYYYMMDD-HHMMSS>
//
// The prefix is the discrimination signal for retention cleanup — a
// manual snapshot won't share it and therefore won't be touched.
func snapshotName(policyID string, at time.Time) string {
	return fmt.Sprintf("%s%s-%s", snapshotNamePrefix, policyID, at.UTC().Format("20060102-150405"))
}

func isPolicySnapshot(policyID, name string) bool {
	return strings.HasPrefix(name, snapshotNamePrefix+policyID+"-")
}

// startSnapshotScheduler runs forever; call once from main. Tick
// cadence is 60 s so a policy with EveryMinutes=1 fires at minute-
// level granularity — fine for the coarsest sensible snapshot
// schedules (hourly / daily / weekly).
func (s *Store) startSnapshotScheduler(ctx context.Context) {
	s.goWork("snapshot_policies", func() {
		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		// Fire once at startup so a freshly-configured policy doesn't
		// wait a whole minute to do its first run.
		s.runDueSnapshotPolicies(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.runDueSnapshotPolicies(ctx)
			}
		}
	})
}

// runDueSnapshotPolicies iterates every policy and fires those whose
// interval has elapsed. Work happens outside the store lock — we copy
// the slice of due policies under the lock, then release it so the
// agent calls don't serialise the whole store.
func (s *Store) runDueSnapshotPolicies(ctx context.Context) {
	// v5.18.8: respect the global disable_snapshots kill-switch. Without
	// this the scheduler kept firing policies + taking snapshots even
	// when the operator had flipped the feature off in General.
	if f := s.GetFeatures(); f.DisableSnapshots {
		return
	}
	now := time.Now().UTC()
	s.mu.RLock()
	due := make([]*models.SnapshotPolicy, 0)
	for _, p := range s.snapshotPolicies {
		if !p.Enabled || p.EveryMinutes <= 0 || len(p.VMUUIDs) == 0 {
			continue
		}
		if p.LastRunAt.IsZero() || now.Sub(p.LastRunAt) >= time.Duration(p.EveryMinutes)*time.Minute {
			cp := *p
			due = append(due, &cp)
		}
	}
	s.mu.RUnlock()

	for _, p := range due {
		s.runOneSnapshotPolicy(ctx, p, now)
	}
}

// runOneSnapshotPolicy snapshots every VM in the policy's target list
// and trims retention. Errors on any target are collected and surfaced
// in LastRunError rather than aborting the whole policy tick.
func (s *Store) runOneSnapshotPolicy(ctx context.Context, p *models.SnapshotPolicy, now time.Time) {
	var errs []string
	created := 0
	for _, uuid := range p.VMUUIDs {
		vm, host := s.lookupVMByUUID(uuid)
		if vm == nil || host == nil {
			errs = append(errs, "vm "+uuid+" not found in inventory")
			continue
		}
		if string(vm.State) != "running" && string(vm.State) != "paused" {
			// Offline snapshot would require domain-xml manipulation
			// the agent doesn't expose; skip with a clear reason so
			// the operator sees why in LastRunError.
			errs = append(errs, "vm "+vm.Name+" skipped (state="+string(vm.State)+")")
			continue
		}
		name := snapshotName(p.ID, now)
		if err := s.agentSnapshotCommand(ctx, host, vm, "SNAPSHOT", map[string]string{"name": name}); err != nil {
			errs = append(errs, vm.Name+": "+err.Error())
			continue
		}
		created++
		auditSystem(s, AuditVMSnapshot, "vm/"+vm.UUID, map[string]string{"policy": p.ID, "name": name}, true, "")

		// Retention: fetch current policy-tagged snapshots and delete
		// the oldest beyond RetentionKeep.
		if p.RetentionKeep > 0 {
			if err := s.pruneSnapshots(ctx, p, host, vm); err != nil {
				errs = append(errs, vm.Name+" prune: "+err.Error())
			}
		}
	}

	// Persist the run metadata under the write lock — short, bounded
	// by the size of LastRunError, safe even with the scheduler tick
	// overlapping a CRUD request.
	s.mu.Lock()
	if stored, ok := s.snapshotPolicies[p.ID]; ok {
		stored.LastRunAt = now
		stored.LastRunCount = created
		stored.LastRunError = strings.Join(errs, "; ")
	}
	s.mu.Unlock()
	s.AsyncSave()
}

// pruneSnapshots enumerates the agent's snapshots for this VM,
// filters to ones created by THIS policy (via the name prefix), and
// deletes all but the most recent RetentionKeep. Manual snapshots
// are never touched — they don't share the prefix.
func (s *Store) pruneSnapshots(ctx context.Context, p *models.SnapshotPolicy, host *models.Host, vm *models.VM) error {
	snaps, err := s.agentListSnapshots(ctx, host, vm)
	if err != nil {
		return err
	}
	var ours []models.Snapshot
	for _, sn := range snaps {
		if isPolicySnapshot(p.ID, sn.Name) {
			ours = append(ours, sn)
		}
	}
	// Newest first by name — our name encodes a UTC timestamp so
	// lexicographic sort works without parsing.
	sort.Slice(ours, func(i, j int) bool { return ours[i].Name > ours[j].Name })
	if len(ours) <= p.RetentionKeep {
		return nil
	}
	doomed := ours[p.RetentionKeep:]
	for _, sn := range doomed {
		if err := s.agentSnapshotCommand(ctx, host, vm, "DELETE_SNAPSHOT", map[string]string{"name": sn.Name}); err != nil {
			return err
		}
		auditSystem(s, AuditVMSnapDelete, "vm/"+vm.UUID, map[string]string{"policy": p.ID, "name": sn.Name, "reason": "retention"}, true, "")
	}
	return nil
}

// lookupVMByUUID walks the store's vms map. Safe to call from the
// scheduler without taking the write lock.
func (s *Store) lookupVMByUUID(uuid string) (*models.VM, *models.Host) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for hostID, vms := range s.vms {
		for _, v := range vms {
			if strings.EqualFold(v.UUID, uuid) {
				return v, s.hosts[hostID]
			}
		}
	}
	return nil, nil
}

// agentSnapshotCommand posts a command to the agent the same way the
// /api/command handler does — just without the HTTP request/response
// wrapper since we're calling from a goroutine.
func (s *Store) agentSnapshotCommand(ctx context.Context, host *models.Host, vm *models.VM, action string, params map[string]string) error {
	addr := s.GetHostAgentAddr(host.ID)
	if addr == "" {
		return fmt.Errorf("agent address unknown for host %s", host.ID)
	}
	body, _ := json.Marshal(models.CommandRequest{
		Action: action,
		VMName: vm.Name,
		VMUUID: vm.UUID,
		Params: params,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/command", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := s.newAgentClient(host.ID, 2*time.Minute)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("agent %s returned %d", action, resp.StatusCode)
	}
	return nil
}

// agentListSnapshots proxies to the agent's /snapshots/<vm> endpoint.
func (s *Store) agentListSnapshots(ctx context.Context, host *models.Host, vm *models.VM) ([]models.Snapshot, error) {
	addr := s.GetHostAgentAddr(host.ID)
	if addr == "" {
		return nil, fmt.Errorf("agent address unknown")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/snapshots/"+vm.Name, nil)
	if err != nil {
		return nil, err
	}
	client := s.newAgentClient(host.ID, 15*time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// Agent ships {"snapshots":[...]} (see agent/snapshots.go), not a
	// bare array — same wrapper the v4 SPA already unwraps.
	var wrapper struct {
		Snapshots []models.Snapshot `json:"snapshots"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrapper); err != nil {
		return nil, err
	}
	return wrapper.Snapshots, nil
}

// ── Store CRUD helpers ──────────────────────────────────────────────

func (s *Store) ListSnapshotPolicies() []*models.SnapshotPolicy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*models.SnapshotPolicy, 0, len(s.snapshotPolicies))
	for _, p := range s.snapshotPolicies {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *Store) CreateSnapshotPolicy(p *models.SnapshotPolicy) error {
	if p.Name == "" {
		return fmt.Errorf("name required")
	}
	if p.EveryMinutes <= 0 {
		return fmt.Errorf("every_minutes must be > 0")
	}
	s.mu.Lock()
	if p.ID == "" {
		p.ID = newPolicyID()
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	s.snapshotPolicies[p.ID] = p
	s.mu.Unlock()
	s.AsyncSave()
	return nil
}

func (s *Store) UpdateSnapshotPolicy(id string, mutate func(*models.SnapshotPolicy) error) error {
	s.mu.Lock()
	p, ok := s.snapshotPolicies[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("policy not found")
	}
	if err := mutate(p); err != nil {
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()
	s.AsyncSave()
	return nil
}

func (s *Store) DeleteSnapshotPolicy(id string) error {
	s.mu.Lock()
	if _, ok := s.snapshotPolicies[id]; !ok {
		s.mu.Unlock()
		return fmt.Errorf("policy not found")
	}
	delete(s.snapshotPolicies, id)
	s.mu.Unlock()
	s.AsyncSave()
	return nil
}

func (s *Store) GetSnapshotPolicy(id string) *models.SnapshotPolicy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p, ok := s.snapshotPolicies[id]; ok {
		cp := *p
		return &cp
	}
	return nil
}

func newPolicyID() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return "pol_" + hex.EncodeToString(b[:])
}

// ── HTTP handlers ──────────────────────────────────────────────────

// Audit actions for policy CRUD. Kept here so the audit dropdown in
// the UI stays single-sourced.
const (
	AuditSnapPolicyCreate = "snapshot.policy.create"
	AuditSnapPolicyUpdate = "snapshot.policy.update"
	AuditSnapPolicyDelete = "snapshot.policy.delete"
	AuditSnapPolicyRun    = "snapshot.policy.run"
)

func handleSnapshotPolicies(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			// GET stays open so the UI can still render the list and
			// show a "snapshots disabled" banner when the flag is on.
			writeJSON(w, http.StatusOK, store.ListSnapshotPolicies())
		case http.MethodPost:
			if featureBlocked(w, store, "snapshots", "snapshot policies") {
				return
			}
			var p models.SnapshotPolicy
			if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
				writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
				return
			}
			if err := store.CreateSnapshotPolicy(&p); err != nil {
				audit(store, r, AuditSnapPolicyCreate, "policy/"+p.Name, nil, false, err.Error())
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			createDetail := map[string]string{
				"name": p.Name, "every_minutes": fmt.Sprintf("%d", p.EveryMinutes), "vm_count": fmt.Sprintf("%d", len(p.VMUUIDs)),
			}
			audit(store, r, AuditSnapPolicyCreate, "policy/"+p.ID, createDetail, true, "")
			recordOperatorTask(store, r, AuditSnapPolicyCreate, "policy/"+p.ID, createDetail, true, "")
			writeJSON(w, http.StatusOK, p)
		default:
			writeError(w, http.StatusMethodNotAllowed, "GET / POST only")
		}
	}
}

// handleSnapshotPolicy handles /api/snapshot-policies/<id> and
// /api/snapshot-policies/<id>/run. A single handler so we only need
// one mux route prefix.
func handleSnapshotPolicy(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/snapshot-policies/")
		runSuffix := "/run"
		runNow := false
		if strings.HasSuffix(id, runSuffix) {
			id = strings.TrimSuffix(id, runSuffix)
			runNow = true
		}
		if id == "" {
			writeError(w, http.StatusBadRequest, "policy id required")
			return
		}
		if runNow {
			if r.Method != http.MethodPost {
				writeError(w, http.StatusMethodNotAllowed, "POST only")
				return
			}
			if featureBlocked(w, store, "snapshots", "snapshot policies") {
				return
			}
			p := store.GetSnapshotPolicy(id)
			if p == nil {
				writeError(w, http.StatusNotFound, "policy not found")
				return
			}
			store.goWork("snapshot-policy", func() { store.runOneSnapshotPolicy(serverContext(), p, time.Now().UTC()) })
			audit(store, r, AuditSnapPolicyRun, "policy/"+id, nil, true, "")
			recordOperatorTask(store, r, AuditSnapPolicyRun, "policy/"+id, map[string]string{"policy_name": p.Name}, true, "")
			writeJSON(w, http.StatusOK, map[string]string{"status": "queued"})
			return
		}
		switch r.Method {
		case http.MethodDelete:
			if featureBlocked(w, store, "snapshots", "snapshot policies") {
				return
			}
			if err := store.DeleteSnapshotPolicy(id); err != nil {
				audit(store, r, AuditSnapPolicyDelete, "policy/"+id, nil, false, err.Error())
				writeError(w, http.StatusNotFound, err.Error())
				return
			}
			audit(store, r, AuditSnapPolicyDelete, "policy/"+id, nil, true, "")
			recordOperatorTask(store, r, AuditSnapPolicyDelete, "policy/"+id, nil, true, "")
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		case http.MethodPut, http.MethodPatch:
			if featureBlocked(w, store, "snapshots", "snapshot policies") {
				return
			}
			var req struct {
				Name          *string   `json:"name,omitempty"`
				Enabled       *bool     `json:"enabled,omitempty"`
				VMUUIDs       *[]string `json:"vm_uuids,omitempty"`
				EveryMinutes  *int      `json:"every_minutes,omitempty"`
				RetentionKeep *int      `json:"retention_keep,omitempty"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
				return
			}
			err := store.UpdateSnapshotPolicy(id, func(p *models.SnapshotPolicy) error {
				if req.Name != nil {
					p.Name = *req.Name
				}
				if req.Enabled != nil {
					p.Enabled = *req.Enabled
				}
				if req.VMUUIDs != nil {
					p.VMUUIDs = *req.VMUUIDs
				}
				if req.EveryMinutes != nil {
					p.EveryMinutes = *req.EveryMinutes
				}
				if req.RetentionKeep != nil {
					p.RetentionKeep = *req.RetentionKeep
				}
				return nil
			})
			if err != nil {
				audit(store, r, AuditSnapPolicyUpdate, "policy/"+id, nil, false, err.Error())
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			audit(store, r, AuditSnapPolicyUpdate, "policy/"+id, nil, true, "")
			recordOperatorTask(store, r, AuditSnapPolicyUpdate, "policy/"+id, nil, true, "")
			writeJSON(w, http.StatusOK, store.GetSnapshotPolicy(id))
		default:
			writeError(w, http.StatusMethodNotAllowed, "PUT / DELETE / POST (for /run) only")
		}
	}
}
