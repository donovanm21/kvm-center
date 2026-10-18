package main

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"kvm-center/internal/models"
)

func TestGuestProjectionFreshnessBoundary(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		stamp time.Time
		fresh bool
	}{
		{"current", now, true}, {"boundary", now.Add(-guestToolsFreshness), true},
		{"expired", now.Add(-guestToolsFreshness - time.Nanosecond), false},
		{"future", now.Add(time.Nanosecond), false}, {"unknown", time.Time{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := guestReportFresh(&guestEntry{UpdatedAt: tc.stamp}, now); got != tc.fresh {
				t.Fatal(got)
			}
		})
	}
	if guestReportFresh(nil, now) {
		t.Fatal("nil report accepted")
	}
}

func TestGuestProjectionExpiryReadRestartPreservesRetainedData(t *testing.T) {
	raw := guestFixture(t, true)
	var report entityGuestReport
	if err := bson.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	vm := &models.VM{UUID: report.UUID, HostID: "fixture", GuestAgent: true, GuestSource: "kvm-tools", GuestHostname: "stale", GuestOS: "old", GuestIPs: []string{"192.0.2.40"}, KVMToolsVersion: "old", KVMToolsUpdateQueued: true}
	s := &Store{vms: map[string][]*models.VM{"fixture": {vm}}, guestData: map[string]*guestEntry{report.UUID: &report.Entry}, kvmToolsUpdateQueue: map[string]bool{report.UUID: true}, entityOverlay: &entityOverlay{Version: 8, Collections: map[string]map[string][]byte{mcoGuestData: {report.UUID: raw}}}}
	before, _ := json.Marshal(s.guestData)
	vmBefore, _ := json.Marshal(vm)
	for _, view := range []*models.VM{s.GetVMs("")[0], s.GetVMs("fixture")[0], s.GetVMByUUID(report.UUID)} {
		if view.GuestAgent || view.GuestSource != "" || view.GuestHostname != "" || len(view.GuestIPs) != 0 || view.KVMToolsVersion != "" || !view.KVMToolsUpdateQueued {
			t.Fatal("expired guest resurrected", view)
		}
	}
	after, _ := json.Marshal(s.guestData)
	vmAfter, _ := json.Marshal(vm)
	if string(before) != string(after) || string(vmBefore) != string(vmAfter) || !s.kvmToolsUpdateQueue[report.UUID] {
		t.Fatal("projection mutated retained data")
	}
	// A backup/restore of the cached projection must not make it current.
	encoded, _ := json.Marshal(&persistData{VMs: s.vms, GuestData: s.guestData, EntityOverlay: s.entityOverlay})
	var restored persistData
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	next := &Store{vms: restored.VMs, guestData: restored.GuestData, entityOverlay: restored.EntityOverlay}
	if next.GetVMByUUID(report.UUID).GuestAgent {
		t.Fatal("restart resurrected expired guest")
	}
	if !reflect.DeepEqual(restored.EntityOverlay.Collections[mcoGuestData][report.UUID], []byte(raw)) {
		t.Fatal("raw BSON changed")
	}
}

func TestGuestProjectionFreshReportAndQEMUIsolation(t *testing.T) {
	now := time.Now()
	id := "fixture"
	vm := &models.VM{UUID: id, GuestAgent: true, GuestSource: "kvm-tools", GuestHostname: "previous", GuestIPs: []string{"192.0.2.40"}}
	s := &Store{guestData: map[string]*guestEntry{id: {UpdatedAt: now, Payload: models.GuestTelemetryPayload{VMUUID: id, Hostname: "current"}}}}
	view := s.guestVMView(vm, now)
	if !view.GuestAgent || view.GuestHostname != "current" || len(view.GuestIPs) != 0 || vm.GuestHostname != "previous" {
		t.Fatal("fresh projection retained stale fields")
	}
	for _, source := range []string{"qemu-ga", "unknown", ""} {
		vm.GuestSource = source
		old := *vm
		s.guestData[id].UpdatedAt = now.Add(-guestToolsFreshness - time.Second)
		view = s.guestVMView(vm, now)
		if !reflect.DeepEqual(*view, old) {
			t.Fatal("unproven source altered", source)
		}
	}
	delete(s.guestData, id)
	vm.GuestSource = "kvm-tools"
	if s.guestVMView(vm, now).GuestAgent {
		t.Fatal("missing report resurrected tools")
	}
}

func TestGuestProjectionCopiesNestedReportSlices(t *testing.T) {
	now := time.Now()
	entry := &guestEntry{UpdatedAt: now, Payload: models.GuestTelemetryPayload{VMUUID: "fixture", IPs: []string{"192.0.2.40"}, FS: []models.GuestFS{{Mountpoint: "/"}}, Interfaces: []models.GuestInterface{{Addrs: []string{"192.0.2.40/24"}}}}}
	s := &Store{guestData: map[string]*guestEntry{"fixture": entry}, vms: map[string][]*models.VM{"host": {{UUID: "fixture"}}}}
	for _, view := range []*models.VM{s.GetVMs("")[0], s.GetVMByUUID("fixture")} {
		view.GuestIPs[0] = "changed"
		view.GuestFS[0].Mountpoint = "changed"
		view.GuestInterfaces[0].Addrs[0] = "changed"
	}
	if entry.Payload.IPs[0] != "192.0.2.40" || entry.Payload.FS[0].Mountpoint != "/" || entry.Payload.Interfaces[0].Addrs[0] != "192.0.2.40/24" {
		t.Fatal("returned view aliases retained report")
	}
	for _, stamp := range []time.Time{time.Time{}, now.Add(time.Hour), now.Add(-time.Hour)} {
		entry.UpdatedAt = stamp
		s.vms["host"][0].GuestSource = "kvm-tools"
		s.vms["host"][0].GuestAgent = true
		if s.GetVMs("")[0].GuestAgent || s.GetVMByUUID("fixture").GuestAgent {
			t.Fatal("unsupported receipt time projected")
		}
	}
}
