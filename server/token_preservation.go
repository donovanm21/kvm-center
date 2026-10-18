package main

import (
	"context"
	"encoding/hex"
	"errors"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"kvm-center/internal/models"
	"reflect"
	"strings"
)

const tokenAdmissionMax = 10000

func validateToken(t *models.APIToken) error {
	if t == nil || t.ID == "" || strings.TrimSpace(t.ID) != t.ID || t.OwnerID == "" || strings.TrimSpace(t.OwnerID) != t.OwnerID {
		return errors.New("ambiguous API token identity or owner")
	}
	hash, err := hex.DecodeString(t.Hash)
	if err != nil || len(hash) != 32 {
		return errors.New("unsupported API token hash encoding")
	}
	if t.Kind != "" && t.Kind != "user" && t.Kind != "system" {
		return errors.New("unsupported API token kind")
	}
	valid := map[string]bool{}
	for _, scope := range models.AllPermissions {
		valid[scope] = true
	}
	for _, scope := range t.Scopes {
		if !valid[scope] {
			return errors.New("unsupported API token scope")
		}
	}
	return nil
}
func validateTokenSnapshot(tokens map[string]*models.APIToken) error {
	if len(tokens) > tokenAdmissionMax {
		return errors.New("API token admission exceeds bound")
	}
	for id, t := range tokens {
		if err := validateToken(t); err != nil {
			return err
		}
		if t.ID != id {
			return errors.New("API token snapshot identity mismatch")
		}
	}
	return nil
}
func validateTokenBSON(raw bson.Raw) error {
	if _, err := rawFields(raw); err != nil {
		return err
	}
	// Only deployed BSON spellings are supported; JSON aliases cannot shadow
	// the owner, expiry or usage timestamps during conversion.
	elements, _ := raw.Elements()
	for _, e := range elements {
		for _, alias := range []string{"owner_id", "created_at", "expires_at", "last_used_at"} {
			if strings.EqualFold(e.Key(), alias) {
				return errors.New("unsupported API token BSON alias")
			}
		}
	}
	var t models.APIToken
	if err := bson.Unmarshal(raw, &t); err != nil {
		return err
	}
	if err := validateToken(&t); err != nil {
		return err
	}
	id, ok := raw.Lookup("_id").StringValueOK()
	if !ok || id != t.ID {
		return errors.New("API token BSON identity mismatch")
	}
	candidate, err := bson.Marshal(struct {
		ID               string `bson:"_id"`
		*models.APIToken `bson:",inline"`
	}{t.ID, &t})
	if err != nil {
		return err
	}
	_, err = preserveEntityBSON(raw, candidate, reflect.TypeOf(t), nil)
	return err
}
func loadTokenRecords(ctx context.Context, db *mongo.Database) (map[string]*models.APIToken, error) {
	cur, err := db.Collection(mcoAPITokens).Find(ctx, bson.D{}, options.Find().SetLimit(tokenAdmissionMax+1))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	out := map[string]*models.APIToken{}
	for cur.Next(ctx) {
		if len(out) == tokenAdmissionMax {
			return nil, errors.New("API token admission exceeds bound")
		}
		if err := validateTokenBSON(cur.Current); err != nil {
			return nil, err
		}
		var t models.APIToken
		if err := bson.Unmarshal(cur.Current, &t); err != nil {
			return nil, err
		}
		if out[t.ID] != nil {
			return nil, errors.New("duplicate API token")
		}
		out[t.ID] = &t
	}
	return out, cur.Err()
}
func copyAPIToken(t *models.APIToken) *models.APIToken {
	if t == nil {
		return nil
	}
	cp := *t
	if t.Scopes != nil {
		cp.Scopes = append([]string{}, t.Scopes...)
	}
	return &cp
}
