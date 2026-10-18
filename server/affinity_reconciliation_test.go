package main

import (
	"context"
	"errors"
	"kvm-center/internal/affinitypolicy"
	"path/filepath"
	"reflect"
	"testing"
)

func affinityReservationFor(id string) affinitypolicy.Reservation {
	r := affinityRequest("192.0.2.3")
	return affinitypolicy.Reservation{RequestID: id, UUID: r.VMUUID, Name: r.VMName, Source: r.HostID, Destination: "target", DestinationAddress: r.Params["destination"]}
}

type affinityAfterSave struct {
	Backend
	after func()
}

func (b *affinityAfterSave) Save(ctx context.Context, d *persistData) error {
	if err := b.Backend.Save(ctx, d); err != nil {
		return err
	}
	if b.after != nil {
		f := b.after
		b.after = nil
		f()
	}
	return nil
}
func TestAffinityPreparedRefusalAllowsNewIdentity(t *testing.T) {
	s, u, calls := affinityStore(t)
	original := s.affinity.Clone()
	s.backend = &affinityAfterSave{Backend: s.backend, after: func() { s.mu.Lock(); s.hosts["target"].MaintenanceMode = true; s.mu.Unlock() }}
	req := affinityRequest("192.0.2.3")
	if w := migrationRequest(s, u, req); w.Code != 409 || calls.Load() != 0 {
		t.Fatalf("refusal %d %s", w.Code, w.Body)
	}
	if s.affinity.Reservation != nil || len(s.affinity.Aborted) != 1 || !reflect.DeepEqual(s.affinity.Originals, original.Originals) {
		t.Fatal("prepared refusal not durably reconciled")
	}
	s.mu.Lock()
	s.hosts["target"].MaintenanceMode = false
	s.mu.Unlock()
	if w := migrationRequest(s, u, req); w.Code != 409 || calls.Load() != 0 {
		t.Fatal("aborted request replayed")
	}
	req.RequestID = "new-attempt"
	if w := migrationRequest(s, u, req); w.Code != 200 || calls.Load() != 1 {
		t.Fatalf("new attempt %d %s", w.Code, w.Body)
	}
	if s.affinity.ReservationPhase != "dispatch_possible" {
		t.Fatal("dispatch not durable")
	}
}
func TestAffinityPreparedRecoveryAndFailureBoundaries(t *testing.T) {
	for _, phase := range []string{"prepared", "dispatch_possible", "aborted"} {
		t.Run(phase, func(t *testing.T) {
			s, _, _ := affinityStore(t)
			q := affinityReservationFor("first")
			_, lease, err := s.reserveAffinity(q)
			if err != nil {
				t.Fatal(err)
			}
			if phase == "dispatch_possible" {
				if err = lease.dispatchPossible(); err != nil {
					t.Fatal(err)
				}
				if err = lease.abortPrepared(); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "aborted" {
				if err = lease.abortPrepared(); err != nil {
					t.Fatal(err)
				}
			}
			s.saveWG.Wait()
			if err = s.SaveChecked(); err != nil {
				t.Fatal(err)
			}
			d, err := s.backend.Load(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			restored := newTestStore(t)
			restored.backend = NewJSONBackend(filepath.Join(t.TempDir(), "restored.json"))
			if err = restored.backend.Save(context.Background(), d); err != nil {
				t.Fatal(err)
			}
			restored.load()
			if !reflect.DeepEqual(restored.affinity, d.Affinity) {
				t.Fatal("restore changed reconciliation ledger")
			}
			if _, _, err = restored.reserveAffinity(q); err == nil {
				t.Fatal("recovered request replayed")
			}
			q.RequestID = "new-attempt"
			_, next, err := restored.reserveAffinity(q)
			if phase == "aborted" {
				if err != nil {
					t.Fatal(err)
				}
				if err = next.abortPrepared(); err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("recovered active reservation released")
			}
		})
	}
	for _, transition := range []string{"abort", "dispatch"} {
		t.Run("failed-"+transition, func(t *testing.T) {
			s, _, _ := affinityStore(t)
			q := affinityReservationFor("first")
			_, lease, err := s.reserveAffinity(q)
			if err != nil {
				t.Fatal(err)
			}
			s.backend = affinityFailBackend{s.backend}
			if transition == "abort" {
				err = lease.abortPrepared()
			} else {
				err = lease.dispatchPossible()
				_ = lease.abortPrepared()
			}
			if err == nil || s.affinity.Reservation == nil {
				t.Fatal("uncertain transition released")
			}
			q.RequestID = "second"
			if _, _, err = s.reserveAffinity(q); err == nil {
				t.Fatal("failure admitted another request")
			}
		})
	}
}

// A backend can commit the reservation transition and still return an error.
type affinityCommitThenFail struct{ Backend }

func (b affinityCommitThenFail) Save(ctx context.Context, d *persistData) error {
	if err := b.Backend.Save(ctx, d); err != nil {
		return err
	}
	return errors.New("synthetic acknowledgement lost")
}
func TestAffinityAbortCommitUnknownKeepsProcessBarrier(t *testing.T) {
	s, _, _ := affinityStore(t)
	q := affinityReservationFor("first")
	_, lease, err := s.reserveAffinity(q)
	if err != nil {
		t.Fatal(err)
	}
	s.backend = affinityCommitThenFail{s.backend}
	if lease.abortPrepared() == nil || s.affinity.Reservation == nil {
		t.Fatal("uncertain abort did not block process")
	}
	d, err := s.backend.Load(context.Background())
	if err != nil || d.Affinity.Reservation != nil || len(d.Affinity.Aborted) != 1 {
		t.Fatal("durable abort not retained", err)
	}
	q.RequestID = "second"
	if _, _, err = s.reserveAffinity(q); err == nil {
		t.Fatal("uncertain process admitted move")
	}
}

func TestAffinityDrainPreparedRefusal(t *testing.T) {
	s, _, calls := affinityStore(t)
	s.backend = &affinityAfterSave{Backend: s.backend, after: func() { s.mu.Lock(); s.hosts["target"].MaintenanceMode = true; s.mu.Unlock() }}
	if err := drainOneVM(context.Background(), s, "source", s.vms["source"][0], s.hosts["target"], "live"); err == nil {
		t.Fatal("changed destination not refused")
	}
	if calls.Load() != 0 || s.affinity.Reservation != nil || len(s.affinity.Aborted) != 1 || len(s.GetMigrations()) != 0 {
		t.Fatal("drain refusal failed to abort before grants/dispatch")
	}
}
