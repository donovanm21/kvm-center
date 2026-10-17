package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"kvm-center/internal/models"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func ledgerRequest(id string) models.CommandRequest {
	return models.CommandRequest{RequestID: id, Action: "START", VMUUID: "11111111-1111-4111-8111-111111111111", VMName: "synthetic-power", Params: map[string]string{"mode": "one"}}
}

func TestLedgerChangedPayloadNeverReplays(t *testing.T) {
	for _, state := range []string{"running", "cached", "durable"} {
		t.Run(state, func(t *testing.T) {
			s := withOpsStore(t)
			resetOutcomes(t)
			req := ledgerRequest("binding-" + state)
			_, _, err := claimCommand(req)
			if err != nil {
				t.Fatal(err)
			}
			if state != "running" {
				executeClaimedCommand(req, func(w http.ResponseWriter, _ models.CommandRequest) { respond(w, true, "done") })
			}
			if state == "durable" {
				resetOutcomes(t)
			}
			for _, field := range []string{"action", "uuid", "name", "params"} {
				changed := req
				switch field {
				case "action":
					changed.Action = "FORCESTOP"
				case "uuid":
					changed.VMUUID = "22222222-2222-4222-8222-222222222222"
				case "name":
					changed.VMName = "other"
				case "params":
					changed.Params = map[string]string{"mode": "two"}
				}
				replay, running, err := claimCommand(changed)
				if !errors.Is(err, errCommandBinding) || replay != nil || running {
					t.Fatalf("%s: replay=%v running=%v err=%v", field, replay, running, err)
				}
			}
			if state == "running" {
				executeClaimedCommand(req, func(w http.ResponseWriter, _ models.CommandRequest) { respond(w, true, "done") })
			}
			var digest string
			if err := s.db.QueryRow(`SELECT params_digest FROM command_admissions WHERE request_id=?`, req.RequestID).Scan(&digest); err != nil || digest == "" || digest == `{"mode":"one"}` {
				t.Fatalf("parameters must be digested: %q %v", digest, err)
			}
		})
	}
}

func TestLedgerRetentionAndRestartCannotRedispatch(t *testing.T) {
	s, path := newTestOpsStore(t)
	old := ops
	ops = s
	defer func() { ops = old }()
	resetOutcomes(t)
	req := ledgerRequest("retained")
	calls := 0
	execute := func(w http.ResponseWriter, _ models.CommandRequest) { calls++; respond(w, true, "done") }
	dispatchForTest(req, execute)
	past := time.Now().Add(-30 * 24 * time.Hour).UnixNano()
	if _, err := s.db.Exec(`UPDATE operations SET completed_at=? WHERE request_id=?`, past, req.RequestID); err != nil {
		t.Fatal(err)
	}
	if n, err := s.GC(context.Background(), 24*time.Hour); err != nil || n != 1 {
		t.Fatalf("GC %d %v", n, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenOperationsStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	ops = reopened
	resetOutcomes(t)
	rr := dispatchForTest(req, execute)
	if calls != 1 || rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("retained tombstone redispatched: calls%d status%d", calls, rr.Code)
	}
	e, err := ops.dispatchEvidence(context.Background(), req.RequestID)
	if err != nil || !e.WorkerReturned {
		t.Fatalf("completion lost: %+v %v", e, err)
	}
}

func TestLedgerFenceInterleavingAndLateCommands(t *testing.T) {
	s := withOpsStore(t)
	resetOutcomes(t)
	req := ledgerRequest("active-start")
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	go func() {
		dispatchForTest(req, func(w http.ResponseWriter, _ models.CommandRequest) {
			calls.Add(1)
			close(started)
			<-release
			respond(w, true, "done")
		})
		close(done)
	}()
	<-started
	if err := s.closeTarget(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	e, err := s.dispatchEvidence(context.Background(), req.RequestID)
	if err != nil || !e.TargetAdmissionClosed || e.PowerDispatchDrained || e.WorkerReturned || e.UnresolvedAdmissions != 1 {
		t.Fatalf("active evidence %+v %v", e, err)
	}
	for _, id := range []string{"late-new-id", ""} {
		late := req
		late.RequestID = id
		rr := dispatchForTest(late, func(w http.ResponseWriter, _ models.CommandRequest) { calls.Add(1); respond(w, true, "unexpected") })
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("late status%d", rr.Code)
		}
	}
	close(release)
	<-done
	e, err = s.dispatchEvidence(context.Background(), req.RequestID)
	if err != nil || !e.PowerDispatchDrained || !e.WorkerReturned || e.NativeQuiescenceVerified || calls.Load() != 1 {
		t.Fatalf("drain evidence %+v %v calls%d", e, err, calls.Load())
	}
	// A native UUID passed in the legacy name slot must also hit the fence.
	alias := req
	alias.RequestID = "alias"
	alias.VMUUID = ""
	alias.VMName = req.VMUUID
	if _, err := s.admitCommand(context.Background(), alias); err == nil {
		t.Fatal("UUID alias bypassed fence")
	}
}

func TestLedgerRestartUnresolvedAndClosed(t *testing.T) {
	s, path := newTestOpsStore(t)
	old := ops
	ops = s
	defer func() { ops = old }()
	resetOutcomes(t)
	req := ledgerRequest("interrupted")
	if fresh, err := s.ledgerClaim(context.Background(), req); err != nil || !fresh {
		t.Fatal(err)
	}
	if err := s.closeTarget(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	s.Close()
	again, err := OpenOperationsStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	ops = again
	resetOutcomes(t)
	e, err := again.dispatchEvidence(context.Background(), req.RequestID)
	if err != nil || e.WorkerReturned || e.PowerDispatchDrained || !e.TargetAdmissionClosed {
		t.Fatalf("restart fabricated completion %+v %v", e, err)
	}
	late := req
	late.RequestID = "after-restart"
	if _, err := again.admitCommand(context.Background(), late); err == nil {
		t.Fatal("restart lost fence")
	}
}

func TestLedgerCancellationDoesNotCertifyWorkerReturn(t *testing.T) {
	s := withOpsStore(t)
	resetOutcomes(t)
	req := ledgerRequest("cancel-active")
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		dispatchForTest(req, func(w http.ResponseWriter, _ models.CommandRequest) {
			close(started)
			<-release
			respond(w, true, "returned")
		})
		close(done)
	}()
	<-started
	deps, _ := cancelTestDeps(t)
	state, err := cancelOperation(context.Background(), req.RequestID, "test request", *deps)
	if err != nil || state != "cancellation_requested" {
		t.Fatalf("cancel %s %v", state, err)
	}
	op, _ := s.Get(context.Background(), req.RequestID)
	e, _ := s.dispatchEvidence(context.Background(), req.RequestID)
	if op.CompletedAt != nil || op.State != OpStateRunning || e.WorkerReturned || !e.CancellationRequested {
		t.Fatalf("premature completion %+v %+v", op, e)
	}
	close(release)
	<-done
	e, _ = s.dispatchEvidence(context.Background(), req.RequestID)
	op, _ = s.Get(context.Background(), req.RequestID)
	if !e.WorkerReturned || op.State != OpStateCancelled {
		t.Fatalf("owner completion missing %+v %+v", op, e)
	}
}

func TestLedgerCompletionWriteFailureIsUnconfirmed(t *testing.T) {
	s := withOpsStore(t)
	resetOutcomes(t)
	req := ledgerRequest("failed-completion")
	rr := dispatchForTest(req, func(w http.ResponseWriter, _ models.CommandRequest) {
		if _, err := s.db.Exec(`CREATE TRIGGER reject_return BEFORE UPDATE OF worker_returned ON command_admissions BEGIN SELECT RAISE(ABORT,'synthetic write failure'); END`); err != nil {
			t.Fatal(err)
		}
		respond(w, true, "native return")
	})
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status%d", rr.Code)
	}
	e, _ := s.dispatchEvidence(context.Background(), req.RequestID)
	if e.WorkerReturned {
		t.Fatal("failed durable write became completion")
	}
}

func TestLedgerUnavailableStoreBlocksLegacyMutation(t *testing.T) {
	old := ops
	ops = nil
	defer func() { ops = old }()
	calls := 0
	req := ledgerRequest("")
	rr := dispatchForTest(req, func(w http.ResponseWriter, _ models.CommandRequest) { calls++ })
	if calls != 0 || rr.Code != 503 {
		t.Fatalf("calls%d status%d", calls, rr.Code)
	}
}

func TestLedgerLegacyImportRemainsUnresolved(t *testing.T) {
	s, path := newTestOpsStore(t)
	req := ledgerRequest("old-row")
	if err := s.Insert(context.Background(), Operation{RequestID: req.RequestID, Action: req.Action, VMUUID: req.VMUUID, VMName: req.VMName, Params: req.Params, State: OpStateSuccess}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	again, err := OpenOperationsStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	e, err := again.dispatchEvidence(context.Background(), req.RequestID)
	if err != nil || e.WorkerReturned {
		t.Fatalf("legacy certainty %+v %v", e, err)
	}
}

func TestPowerTargetBinding(t *testing.T) {
	req := ledgerRequest("target")
	look := func(verb, target string) (string, error) {
		if verb == "domuuid" {
			return req.VMUUID, nil
		}
		return req.VMName, nil
	}
	got, err := resolveVMTarget(req, look)
	if err != nil || got.VMUUID != req.VMUUID {
		t.Fatal(err)
	}
	mismatch := req
	mismatch.VMName = "reused-name"
	if _, err := resolveVMTarget(mismatch, look); err == nil {
		t.Fatal("name mismatch accepted")
	}
	mismatch = req
	mismatch.VMUUID = "22222222-2222-4222-8222-222222222222"
	if _, err := resolveVMTarget(mismatch, look); err == nil {
		t.Fatal("UUID mismatch accepted")
	}
	nameOnly := req
	nameOnly.VMUUID = ""
	got, err = resolveVMTarget(nameOnly, look)
	if err != nil || got.VMUUID != req.VMUUID {
		t.Fatalf("name resolution %+v %v", got, err)
	}
}

func TestLedgerOutcomeEvidenceDoesNotClaimNativeQuiescence(t *testing.T) {
	s := withOpsStore(t)
	resetOutcomes(t)
	req := ledgerRequest("read-evidence")
	dispatchForTest(req, func(w http.ResponseWriter, _ models.CommandRequest) { respond(w, true, "done") })
	s.closeTarget(context.Background(), req)
	rr := doGet(t, "/command-outcome?request_id="+req.RequestID)
	var body struct {
		Dispatch DispatchEvidence `json:"dispatch"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Dispatch.PowerDispatchDrained || body.Dispatch.NativeQuiescenceVerified {
		t.Fatalf("wrong evidence %+v", body)
	}
}

func TestLedgerReplayDoesNotResolveDeletedNativeTarget(t *testing.T) {
	withOpsStore(t)
	resetOutcomes(t)
	req := ledgerRequest("removed-target")
	lookups, calls := 0, 0
	invoke := func() {
		raw, _ := json.Marshal(req)
		w := httptest.NewRecorder()
		handleCommandWithResolver(w, httptest.NewRequest(http.MethodPost, "/command", bytes.NewReader(raw)), func(w http.ResponseWriter, _ models.CommandRequest) { calls++; respond(w, true, "done") }, func(r models.CommandRequest) (models.CommandRequest, error) {
			lookups++
			if lookups > 1 {
				return r, errors.New("domain no longer exists")
			}
			return r, nil
		})
		if w.Code != 200 {
			t.Fatalf("status%d %s", w.Code, w.Body)
		}
	}
	invoke()
	invoke()
	if lookups != 1 || calls != 1 {
		t.Fatalf("replay resolved/dispatched again: %d %d", lookups, calls)
	}
}

func TestLedgerLegacyUUIDAliasPreventsDrain(t *testing.T) {
	s := withOpsStore(t)
	resetOutcomes(t)
	req := ledgerRequest("alias-evidence")
	dispatchForTest(req, func(w http.ResponseWriter, _ models.CommandRequest) { respond(w, true, "done") })
	legacy := Operation{RequestID: "legacy-alias", Action: "START", VMName: strings.ToUpper(req.VMUUID), State: OpStateSuccess}
	if err := s.Insert(context.Background(), legacy); err != nil {
		t.Fatal(err)
	}
	if err := applyCommandLedger(context.Background(), s.db); err != nil {
		t.Fatal(err)
	}
	if err := s.closeTarget(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	e, err := s.dispatchEvidence(context.Background(), req.RequestID)
	if err != nil || e.PowerDispatchDrained || e.UnresolvedAdmissions != 1 {
		t.Fatalf("legacy alias omitted %+v %v", e, err)
	}
}

func TestLedgerOnlyEvidenceDoesNotInventOutcome(t *testing.T) {
	s := withOpsStore(t)
	resetOutcomes(t)
	req := ledgerRequest("expired-evidence")
	dispatchForTest(req, func(w http.ResponseWriter, _ models.CommandRequest) { respond(w, true, "done") })
	s.closeTarget(context.Background(), req)
	if _, err := s.db.Exec(`DELETE FROM operations WHERE request_id=?`, req.RequestID); err != nil {
		t.Fatal(err)
	}
	resetOutcomes(t)
	rr := doGet(t, "/command-outcome?request_id="+req.RequestID)
	var body struct {
		Outcome  string           `json:"outcome"`
		Dispatch DispatchEvidence `json:"dispatch"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rr.Code != 200 || body.Outcome != "unknown" || !body.Dispatch.PowerDispatchDrained {
		t.Fatalf("retained evidence %+v status%d", body, rr.Code)
	}
}

func TestLedgerFencesAllVMTargetAliases(t *testing.T) {
	s := withOpsStore(t)
	resetOutcomes(t)
	fixture := ledgerRequest("fixture")
	if err := s.closeTarget(context.Background(), fixture); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"POWER_CYCLE", "DELETE_VM", "EDIT_VM", "SNAPSHOT"} {
		for _, alias := range []string{"42", "renamed-fixture"} {
			req := models.CommandRequest{RequestID: action + alias, Action: action, VMName: alias}
			raw, _ := json.Marshal(req)
			calls := 0
			w := httptest.NewRecorder()
			handleCommandWithResolver(w, httptest.NewRequest(http.MethodPost, "/command", bytes.NewReader(raw)), func(w http.ResponseWriter, _ models.CommandRequest) { calls++; respond(w, true, "unexpected") }, func(req models.CommandRequest) (models.CommandRequest, error) {
				// Numeric IDs resolve to the current canonical name. Renamed names resolve
				// to the same stable UUID and must also be refused by the permanent fence.
				return resolveVMTarget(req, func(verb, target string) (string, error) {
					if verb == "domuuid" {
						return fixture.VMUUID, nil
					}
					if alias == "42" {
						return "42", nil
					}
					return alias, nil
				})
			})
			if calls != 0 || w.Code < 400 {
				t.Fatalf("%s/%s bypassed fence: calls%d status%d", action, alias, calls, w.Code)
			}
		}
	}
}

func TestLedgerUnknownLegacyNumericTargetPreventsDrain(t *testing.T) {
	s := withOpsStore(t)
	resetOutcomes(t)
	req := ledgerRequest("numeric-proof")
	dispatchForTest(req, func(w http.ResponseWriter, _ models.CommandRequest) { respond(w, true, "done") })
	s.closeTarget(context.Background(), req)
	if err := s.Insert(context.Background(), Operation{RequestID: "old-numeric", Action: "POWER_CYCLE", VMName: "42", State: OpStateSuccess}); err != nil {
		t.Fatal(err)
	}
	if err := applyCommandLedger(context.Background(), s.db); err != nil {
		t.Fatal(err)
	}
	e, err := s.dispatchEvidence(context.Background(), req.RequestID)
	if err != nil || e.PowerDispatchDrained || e.UnresolvedAdmissions != 1 {
		t.Fatalf("unattributed legacy omitted %+v %v", e, err)
	}
}

func TestLedgerActiveOutcomeRemainsRunning(t *testing.T) {
	withOpsStore(t)
	resetOutcomes(t)
	req := ledgerRequest("active-lookup")
	if _, _, err := claimCommand(req); err != nil {
		t.Fatal(err)
	}
	rr := doGet(t, "/command-outcome?request_id="+req.RequestID)
	var body models.CommandOutcomeResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Outcome != "running" {
		t.Fatalf("active request falsely terminal %+v", body)
	}
	executeClaimedCommand(req, func(w http.ResponseWriter, _ models.CommandRequest) { respond(w, true, "done") })
}

func TestResolvedOfflineMigrationPreservesDestinationName(t *testing.T) {
	prior := sshStrictMode.Load()
	t.Cleanup(func() { setSSHStrict(prior) })
	req := ledgerRequest("migration-name")
	req.Action = "OFFLINE_MIGRATE"
	req.Params = map[string]string{"destination": "192.0.2.20"}
	resolved, err := resolveVMTarget(req, func(verb, target string) (string, error) {
		if verb == "domuuid" {
			return req.VMUUID, nil
		}
		return req.VMName, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	w := httptest.NewRecorder()
	handleOfflineMigrateWithStarter(w, resolved, commandExecutionTarget(resolved),
		func(source, dest, id string, keep bool, transport string, opts MTLSOptions) {
			calls++
			finalName := opts.NewVMName
			if finalName == "" {
				finalName = source
			}
			if source != req.VMName || finalName != req.VMName || dest != "192.0.2.20" {
				t.Fatalf("migration naming changed: source=%q destination=%q host=%q", source, finalName, dest)
			}
		})
	if calls != 1 {
		t.Fatalf("migration did not start: %s", w.Body)
	}
	resolved.Action = "START"
	if commandExecutionTarget(resolved) != req.VMUUID {
		t.Fatal("power target is not stable UUID")
	}
}
