package main

import (
	"context"
	"encoding/json"
	"errors"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"kvm-center/internal/affinitypolicy"
	"kvm-center/internal/models"
	"net"
	"reflect"
	"strings"
	"time"
)

func loadAffinityState(ctx context.Context, db *mongo.Database) (*affinitypolicy.State, error) {
	cur, err := db.Collection("affinity_state").Find(ctx, bson.D{}, options.Find().SetLimit(2))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var state *affinitypolicy.State
	for cur.Next(ctx) {
		if state != nil {
			return nil, errors.New("duplicate affinity singleton")
		}
		r := cur.Current
		if err := strictBSONKeys(r, "_id", "payload"); err != nil {
			return nil, err
		}
		id, ok := r.Lookup("_id").StringValueOK()
		if !ok || id != "singleton" {
			return nil, errors.New("invalid affinity singleton")
		}
		payload, ok := r.Lookup("payload").StringValueOK()
		if !ok {
			return nil, errors.New("invalid affinity payload")
		}
		state = &affinitypolicy.State{}
		if err := json.Unmarshal([]byte(payload), state); err != nil {
			return nil, err
		}
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	// Never permit a timer or custom uniqueness policy on the reservation ledger.
	indexes, err := db.Collection("affinity_state").Indexes().List(ctx)
	if err != nil {
		var ce mongo.CommandError
		if errors.As(err, &ce) && ce.Code == 26 {
			return state, nil
		}
		return nil, err
	}
	defer indexes.Close(ctx)
	for indexes.Next(ctx) {
		r := indexes.Current
		if err := strictBSONKeys(r, "v", "key", "name", "ns"); err != nil {
			return nil, err
		}
		key, ok := r.Lookup("key").DocumentOK()
		if !ok {
			return nil, errors.New("invalid affinity index")
		}
		els, e := key.Elements()
		name, _ := r.Lookup("name").StringValueOK()
		dir, good := key.Lookup("_id").Int32OK()
		if e != nil || len(els) != 1 || name != "_id_" || !good || dir != 1 {
			return nil, errors.New("unsupported affinity index")
		}
	}
	return state, indexes.Err()
}
func preflightAffinitySave(ctx context.Context, db *mongo.Database, d *persistData) error {
	if err := d.Affinity.Validate(); err != nil {
		return err
	}
	old, err := loadAffinityState(ctx, db)
	if err != nil {
		return err
	}
	if old == nil && d.Affinity != nil && !d.restoreLegacyHistory {
		return errors.New("affinity installation requires explicit fresh-destination restore")
	}
	return affinitypolicy.ValidateTransition(old, d.Affinity)
}
func saveAffinityState(ctx context.Context, db *mongo.Database, d *persistData) error {
	if d.Affinity == nil || !isDirty(d, "affinity_state") {
		return nil
	}
	raw, err := json.Marshal(d.Affinity)
	if err != nil {
		return err
	}
	_, err = db.Collection("affinity_state").ReplaceOne(ctx, bson.D{{Key: "_id", Value: "singleton"}}, bson.D{{Key: "_id", Value: "singleton"}, {Key: "payload", Value: string(raw)}}, options.Replace().SetUpsert(true))
	return err
}
func (s *Store) affinityActive() bool { s.mu.RLock(); defer s.mu.RUnlock(); return s.affinity != nil }

// affinityViewLocked refuses stale or ambiguous fleet information rather than
// treating offline/missing members as available placement capacity.
func (s *Store) affinityViewLocked(q affinitypolicy.Reservation, now time.Time) ([]string, error) {
	if s.affinity == nil {
		return nil, nil
	}
	dest := s.hosts[q.Destination]
	if dest == nil || dest.Status != models.HostOnline || dest.MaintenanceMode || dest.IP != q.DestinationAddress {
		return nil, errors.New("affinity destination must be online, enrolled and outside maintenance")
	}
	for id, h := range s.hosts {
		if h != nil && id != q.Destination && net.ParseIP(h.IP).Equal(net.ParseIP(q.DestinationAddress)) {
			return nil, errors.New("ambiguous affinity destination address")
		}
	}
	var in []affinitypolicy.Placement
	for id, h := range s.hosts {
		if h == nil || h.ID != id || h.LastSeen.IsZero() || now.Sub(h.LastSeen) > 2*time.Minute || h.LastSeen.After(now.Add(time.Minute)) {
			return nil, errors.New("affinity requires unambiguous fresh fleet inventory")
		}
	}
	for host, vms := range s.vms {
		if s.hosts[host] == nil {
			return nil, errors.New("affinity inventory references unknown host")
		}
		for _, v := range vms {
			if v == nil || v.HostID != host {
				return nil, errors.New("nil affinity VM")
			}
			in = append(in, affinitypolicy.Placement{UUID: v.UUID, Name: v.Name, Host: host})
		}
	}
	return s.affinity.Check(in, q)
}
func (s *Store) affinityDestination(destination string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if net.ParseIP(destination) == nil {
		return "", errors.New("affinity requires an enrolled IP address, not a host name or ID")
	}
	found := ""
	for id, h := range s.hosts {
		if h != nil && net.ParseIP(h.IP).Equal(net.ParseIP(destination)) {
			if found != "" && found != id {
				return "", errors.New("ambiguous affinity destination")
			}
			found = id
		}
	}
	if found == "" {
		return "", errors.New("unknown affinity destination")
	}
	return found, nil
}

// Prepare before grants or dispatch. Only the returned process-local owner can
// abort a prepared reservation; native outcomes never authorize release.
func (s *Store) reserveAffinity(q affinitypolicy.Reservation) ([]string, *affinityLease, error) {
	s.affinityMu.Lock()
	defer s.affinityMu.Unlock()
	s.mu.Lock()
	if s.affinity == nil {
		s.mu.Unlock()
		return nil, nil, nil
	}
	warnings, err := s.affinityViewLocked(q, time.Now())
	if err != nil {
		s.mu.Unlock()
		return nil, nil, err
	}
	for _, m := range s.migrations {
		if m != nil && !isTerminalMigration(m.Status) {
			s.mu.Unlock()
			return nil, nil, errors.New("existing migration uncertainty blocks affinity admission")
		}
	}
	if s.commandLog != nil {
		for _, c := range s.commandLog.snapshot() {
			a := strings.ToUpper(c.Action)
			if (a == "MIGRATE" || a == "OFFLINE_MIGRATE") && (c.Outcome == "pending" || c.Outcome == "unknown") {
				s.mu.Unlock()
				return nil, nil, errors.New("existing command uncertainty blocks affinity admission")
			}
		}
	}
	next := s.affinity.Clone()
	next.Version = 2
	next.ReservationPhase = "prepared"
	next.Reservation = &q
	// Reserve enough ledger capacity for an abort before admitting the owner.
	aborted := next.Clone()
	aborted.Reservation = nil
	aborted.ReservationPhase = ""
	if aborted.Aborted == nil {
		aborted.Aborted = map[string]affinitypolicy.Reservation{}
	}
	aborted.Aborted[q.RequestID] = q
	if err := next.Validate(); err != nil {
		s.mu.Unlock()
		return nil, nil, err
	}
	if err := aborted.Validate(); err != nil {
		s.mu.Unlock()
		return nil, nil, err
	}
	s.affinity = next
	s.mu.Unlock()
	if err := s.SaveChecked(); err != nil {
		return nil, nil, errors.New("affinity reservation durability uncertain; dispatch refused and reservation retained")
	}
	return warnings, &affinityLease{store: s, reservation: q}, nil
}
func (s *Store) recheckAffinity(q affinitypolicy.Reservation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.affinity == nil {
		return nil
	}
	if !reflect.DeepEqual(s.affinity.Reservation, &q) {
		return errors.New("affinity reservation mismatch")
	}
	saved := s.affinity
	s.affinity = saved.Clone()
	s.affinity.Reservation = nil
	s.affinity.ReservationPhase = ""
	_, err := s.affinityViewLocked(q, time.Now())
	s.affinity = saved
	return err
}

// A lease is deliberately process-local and never reconstructed from a backup.
// The sole handler owner may abort before dispatch becomes possible. Recovered
// prepared reservations conservatively remain blocked.
type affinityLease struct {
	store       *Store
	reservation affinitypolicy.Reservation
	crossed     bool
}

func (l *affinityLease) dispatchPossible() error {
	if l == nil {
		return nil
	}
	// Set before persistence: an uncertain write must never permit deferred abort.
	l.crossed = true
	s := l.store
	s.affinityMu.Lock()
	defer s.affinityMu.Unlock()
	s.mu.Lock()
	if s.affinity == nil || s.affinity.Version != 2 || s.affinity.ReservationPhase != "prepared" || !reflect.DeepEqual(s.affinity.Reservation, &l.reservation) {
		s.mu.Unlock()
		return errors.New("affinity preparation ownership lost")
	}
	next := s.affinity.Clone()
	next.ReservationPhase = "dispatch_possible"
	s.affinity = next
	s.mu.Unlock()
	if err := s.SaveChecked(); err != nil {
		return errors.New("affinity dispatch transition uncertain; no grant or command permitted")
	}
	return nil
}
func (l *affinityLease) abortPrepared() error {
	if l == nil || l.crossed {
		return nil
	}
	s := l.store
	s.affinityMu.Lock()
	defer s.affinityMu.Unlock()
	s.mu.Lock()
	if s.affinity == nil || s.affinity.Version != 2 || s.affinity.ReservationPhase != "prepared" || !reflect.DeepEqual(s.affinity.Reservation, &l.reservation) {
		s.mu.Unlock()
		return errors.New("affinity preparation ownership lost")
	}
	old := s.affinity
	next := old.Clone()
	if next.Aborted == nil {
		next.Aborted = map[string]affinitypolicy.Reservation{}
	}
	next.Aborted[l.reservation.RequestID] = l.reservation
	next.Reservation = nil
	next.ReservationPhase = ""
	s.affinity = next
	s.mu.Unlock()
	if err := s.SaveChecked(); err != nil {
		// The write may have committed. Retain an in-memory barrier and do not
		// retry execution; recovery can use the durable aborted identity if present.
		s.mu.Lock()
		s.affinity = old
		s.mu.Unlock()
		return errors.New("affinity abort durability uncertain; admission remains blocked")
	}
	return nil
}
