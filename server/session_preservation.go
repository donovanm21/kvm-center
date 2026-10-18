package main

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"kvm-center/internal/models"
	"reflect"
	"strings"
)

const sessionAdmissionMax = 10000

func validateSession(s *models.Session) error {
	if s == nil || s.Token == "" || strings.TrimSpace(s.Token) != s.Token || s.UserID == "" || strings.TrimSpace(s.UserID) != s.UserID {
		return errors.New("ambiguous session identity or owner")
	}
	return nil
}
func validateSessionSnapshot(sessions map[string]*models.Session) error {
	if len(sessions) > sessionAdmissionMax {
		return errors.New("session admission exceeds bound")
	}
	for key, s := range sessions {
		if err := validateSession(s); err != nil {
			return err
		}
		if s.Token != key {
			return errors.New("session snapshot identity mismatch")
		}
	}
	return nil
}
func validateSessionBSON(raw bson.Raw) error {
	if _, err := rawFields(raw); err != nil {
		return err
	}
	elements, _ := raw.Elements()
	for _, e := range elements {
		for _, alias := range []string{"user_id", "created_at", "user_agent"} {
			if strings.EqualFold(e.Key(), alias) {
				return errors.New("unsupported session BSON alias")
			}
		}
	}
	var s models.Session
	if err := bson.Unmarshal(raw, &s); err != nil {
		return err
	}
	if err := validateSession(&s); err != nil {
		return err
	}
	id, ok := raw.Lookup("_id").StringValueOK()
	if !ok || id != s.Token {
		return errors.New("session BSON identity mismatch")
	}
	candidate, err := bson.Marshal(struct {
		ID              string `bson:"_id"`
		*models.Session `bson:",inline"`
	}{s.Token, &s})
	if err != nil {
		return err
	}
	_, err = preserveEntityBSON(raw, candidate, reflect.TypeOf(s), map[string]string{"expiresat": "expires_at"})
	return err
}
func loadSessionRecords(ctx context.Context, db *mongo.Database) (map[string]*models.Session, error) {
	cur, err := db.Collection(mcoSessions).Find(ctx, bson.D{}, options.Find().SetLimit(sessionAdmissionMax+1))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	out := map[string]*models.Session{}
	for cur.Next(ctx) {
		if len(out) == sessionAdmissionMax {
			return nil, errors.New("session admission exceeds bound")
		}
		if err := validateSessionBSON(cur.Current); err != nil {
			return nil, err
		}
		var s models.Session
		if err := bson.Unmarshal(cur.Current, &s); err != nil {
			return nil, err
		}
		if out[s.Token] != nil {
			return nil, errors.New("duplicate session")
		}
		out[s.Token] = &s
	}
	return out, cur.Err()
}
