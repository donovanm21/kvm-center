package main

import (
	"testing"
	"time"

	"kvm-center/internal/models"
)

// Audit filter unit tests (v5.12.0). The UI dropdown relies on
// filter.matches() returning consistent results; a mis-implemented
// substring vs equality check there makes compliance search produce
// wrong answers.

func TestAuditFilter_MatchesAction(t *testing.T) {
	ev := &models.AuditEvent{Action: "vm.snapshot", ActorID: "u1"}
	if !(AuditFilter{Action: "vm.snapshot"}).matches(ev) {
		t.Error("action equality failed")
	}
	if (AuditFilter{Action: "vm.other"}).matches(ev) {
		t.Error("action mismatch still matched")
	}
	// Filter uses EqualFold — case-insensitive.
	if !(AuditFilter{Action: "VM.SNAPSHOT"}).matches(ev) {
		t.Error("action should be case-insensitive")
	}
}

func TestAuditFilter_ResourceIsSubstring(t *testing.T) {
	ev := &models.AuditEvent{Resource: "vm/0913b752-78ec-448a-a6d7-1dac247c60c6"}
	if !(AuditFilter{Resource: "0913b752"}).matches(ev) {
		t.Error("substring resource match failed")
	}
	if (AuditFilter{Resource: "hvnode02"}).matches(ev) {
		t.Error("unrelated substring matched")
	}
}

func TestAuditFilter_DateRange(t *testing.T) {
	now := time.Now()
	ev := &models.AuditEvent{At: now, Action: "x"}
	if (AuditFilter{Since: now.Add(time.Hour)}).matches(ev) {
		t.Error("future since-filter matched past event")
	}
	if (AuditFilter{Until: now.Add(-time.Hour)}).matches(ev) {
		t.Error("past until-filter matched future event")
	}
	if !(AuditFilter{Since: now.Add(-time.Hour), Until: now.Add(time.Hour)}).matches(ev) {
		t.Error("event inside range didn't match")
	}
}

func TestAuditFilter_SuccessTristate(t *testing.T) {
	passed := &models.AuditEvent{Success: true}
	failed := &models.AuditEvent{Success: false}
	trueVal := true
	falseVal := false
	if !(AuditFilter{Success: &trueVal}).matches(passed) {
		t.Error("success=true didn't match passed event")
	}
	if (AuditFilter{Success: &trueVal}).matches(failed) {
		t.Error("success=true matched failed event")
	}
	if !(AuditFilter{Success: &falseVal}).matches(failed) {
		t.Error("success=false didn't match failed event")
	}
	// nil Success matches both outcomes.
	if !(AuditFilter{}).matches(passed) || !(AuditFilter{}).matches(failed) {
		t.Error("nil Success should match any outcome")
	}
}

func TestAddAuditEvent_RingBufferTrim(t *testing.T) {
	s := NewStore(t.TempDir() + "/data.json")
	for i := 0; i < auditRetentionMax+50; i++ {
		s.AddAuditEvent(&models.AuditEvent{Action: "test"})
	}
	evs, _ := s.GetAuditEvents(AuditFilter{Limit: auditRetentionMax + 100})
	if len(evs) > auditRetentionMax {
		t.Errorf("audit log exceeded retention cap: %d > %d", len(evs), auditRetentionMax)
	}
}
