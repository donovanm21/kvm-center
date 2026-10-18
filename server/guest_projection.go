package main

import (
	"strings"
	"time"

	"kvm-center/internal/models"
)

// Receipt time, not the guest's clock, defines the retained five-minute window.
// Unknown/future receipt times cannot establish current guest availability.
func guestReportFresh(entry *guestEntry, now time.Time) bool {
	return entry != nil && !entry.UpdatedAt.IsZero() && !entry.UpdatedAt.After(now) && now.Sub(entry.UpdatedAt) <= guestToolsFreshness
}

// guestVMView projects onto a copy while the caller holds the store read lock.
// Expiry changes availability only: original reports, cached inventory, opaque
// BSON, timestamps and queued instructions remain untouched for recovery.
func (s *Store) guestVMView(v *models.VM, now time.Time) *models.VM {
	if v == nil {
		return nil
	}
	cp := *v
	entry := s.guestData[strings.ToLower(v.UUID)]
	if cp.GuestSource == "kvm-tools" {
		clearStaleGuestData(&cp)

	}
	if guestReportFresh(entry, now) {
		s.applyVMSideDataAt(&cp, now)
	}
	// Queue visibility is independent of report availability. This never consumes it.
	cp.KVMToolsUpdateQueued = s.guestUpdateQueuedLocked(strings.ToLower(v.UUID))
	cp.GuestIPs = append([]string(nil), cp.GuestIPs...)
	cp.GuestFS = append([]models.GuestFS(nil), cp.GuestFS...)
	cp.GuestInterfaces = append([]models.GuestInterface(nil), cp.GuestInterfaces...)
	for i := range cp.GuestInterfaces {
		cp.GuestInterfaces[i].Addrs = append([]string(nil), cp.GuestInterfaces[i].Addrs...)
	}
	return &cp
}
