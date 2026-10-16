package main

import (
	"context"
	"errors"
	"testing"

	"kvm-center/internal/models"
)

// Emulates Mongo's collection-by-collection Save: one dirty collection has
// reached the database, but a later collection fails the overall Save.
type partialSaveBackend struct {
	stubBackend
	roles          map[string]*models.Role
	failAfterRoles bool
}

func (b *partialSaveBackend) Save(_ context.Context, d *persistData) error {
	if isDirty(d, "roles") {
		b.roles = d.Roles
	}
	if b.failAfterRoles {
		return errors.New("failure after roles committed")
	}
	return nil
}
func TestSaveChecked_PartialSaveThenReversionRepairsBackend(t *testing.T) {
	b := &partialSaveBackend{stubBackend: stubBackend{name: "partial"}}
	s := &Store{backend: b, saveGateOpen: true, commandLog: NewCommandLog(), roles: map[string]*models.Role{"r": {ID: "r", Name: "original"}}}
	if err := s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	s.roles["r"].Name = "temporary"
	b.failAfterRoles = true
	if err := s.SaveChecked(); err == nil {
		t.Fatal("expected injected error")
	}
	s.roles["r"].Name = "original"
	b.failAfterRoles = false
	if err := s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	if b.roles["r"].Name != "original" {
		t.Fatalf("successful retry left partially committed role %q; want original", b.roles["r"].Name)
	}
}
