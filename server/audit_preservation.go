package main

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"kvm-center/internal/models"
	"reflect"
	"sort"
	"strings"
	"time"
)

// Audit history has a deterministic chronological order across both backends.
// ID breaks timestamp ties; no original timestamp is refreshed on admission.
func auditBefore(a, b *models.AuditEvent) bool {
	if a.At.Truncate(time.Millisecond).Equal(b.At.Truncate(time.Millisecond)) {
		return a.ID < b.ID
	}
	return a.At.Before(b.At)
}
func sortAudit(events []*models.AuditEvent) {
	sort.Slice(events, func(i, j int) bool { return auditBefore(events[i], events[j]) })
}
func insertAudit(events []*models.AuditEvent, ev *models.AuditEvent) []*models.AuditEvent {
	at := sort.Search(len(events), func(i int) bool { return !auditBefore(events[i], ev) })
	events = append(events, nil)
	copy(events[at+1:], events[at:])
	events[at] = ev
	return events
}
func validateAuditSnapshot(events []*models.AuditEvent) error {
	if len(events) > auditRetentionMax {
		return errors.New("audit admission exceeds active retention bound; retain originals and explicitly partition offline")
	}
	seen := map[string]bool{}
	for _, ev := range events {
		if ev == nil || ev.ID == "" || strings.TrimSpace(ev.ID) != ev.ID || seen[ev.ID] || ev.At.IsZero() {
			return errors.New("ambiguous audit identity or timestamp")
		}
		seen[ev.ID] = true
	}
	return nil
}
func validateAuditBSON(raw bson.Raw) error {
	_, err := rawFields(raw)
	if err != nil {
		return err
	}
	elements, _ := raw.Elements()
	for _, element := range elements {
		for _, alias := range []string{"actorid", "actor_name", "actor_ip"} {
			if strings.EqualFold(element.Key(), alias) && !(element.Key() == "actorid" && alias == "actorid") {
				return errors.New("unsupported audit BSON alias")
			}
		}
	}
	var ev models.AuditEvent
	if err := bson.Unmarshal(raw, &ev); err != nil {
		return err
	}
	id, ok := raw.Lookup("_id").StringValueOK()
	if !ok || id != ev.ID {
		return errors.New("audit identity mismatch")
	}
	if err := validateAuditSnapshot([]*models.AuditEvent{&ev}); err != nil {
		return err
	}
	candidate, err := bson.Marshal(struct {
		ID                 string `bson:"_id"`
		*models.AuditEvent `bson:",inline"`
	}{ev.ID, &ev})
	if err != nil {
		return err
	}
	_, err = preserveEntityBSON(raw, candidate, reflect.TypeOf(ev), map[string]string{"actorid": "actor_id"})
	return err
}
func loadAuditRecords(ctx context.Context, db *mongo.Database) ([]*models.AuditEvent, error) {
	cur, err := db.Collection(mcoAuditEvents).Find(ctx, bson.D{}, options.Find().SetLimit(auditRetentionMax+1))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	events := []*models.AuditEvent{}
	for cur.Next(ctx) {
		if len(events) == auditRetentionMax {
			return nil, errors.New("audit admission exceeds active retention bound")
		}
		if err := validateAuditBSON(cur.Current); err != nil {
			return nil, err
		}
		var ev models.AuditEvent
		if err := bson.Unmarshal(cur.Current, &ev); err != nil {
			return nil, err
		}
		events = append(events, &ev)
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	if err := validateAuditSnapshot(events); err != nil {
		return nil, err
	}
	sortAudit(events)
	return events, nil
}
