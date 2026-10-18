package main

// Storage-only JSON encoding. API model JSON deliberately omits credential
// fields, but backups and the JSON backend must retain them. Keep those
// fields in a private sidecar without changing the public model encoders.
// Older snapshots without the sidecar continue to decode normally.

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"
)

type persistPrivate struct {
	GatewayToken  string                              `json:"gateway_token,omitempty"`
	Enrollments   map[string]persistEnrollmentPrivate `json:"enrollments,omitempty"`
	CommandExpiry map[string]*time.Time               `json:"command_expiry,omitempty"`
}

type persistEnrollmentPrivate struct {
	PollTokenHash string `json:"poll_token_hash,omitempty"`
	IssuedCertPEM string `json:"issued_cert_pem,omitempty"`
	IssuedCAPEM   string `json:"issued_ca_pem,omitempty"`
}

type persistDataAlias persistData

type persistWire struct {
	*persistDataAlias
	Private *persistPrivate `json:"_private,omitempty"`
}

func (d persistData) MarshalJSON() ([]byte, error) {
	overlay, err := reconcileOverlay(&d, d.EntityOverlay)
	if err != nil {
		return nil, err
	}
	d.EntityOverlay = overlay
	private := &persistPrivate{}
	if d.DiscordGateway != nil {
		private.GatewayToken = d.DiscordGateway.Token
	}
	for id, p := range d.PendingEnrollments {
		if p == nil || (p.PollTokenHash == "" && p.IssuedCertPEM == "" && p.IssuedCAPEM == "") {
			continue
		}
		if private.Enrollments == nil {
			private.Enrollments = make(map[string]persistEnrollmentPrivate)
		}
		private.Enrollments[id] = persistEnrollmentPrivate{p.PollTokenHash, p.IssuedCertPEM, p.IssuedCAPEM}
	}
	for id, c := range d.CommandLog {
		if c == nil || c.ExpiresAt == nil {
			continue
		}
		if private.CommandExpiry == nil {
			private.CommandExpiry = make(map[string]*time.Time)
		}
		private.CommandExpiry[id] = c.ExpiresAt
	}
	if private.GatewayToken == "" && len(private.Enrollments) == 0 && len(private.CommandExpiry) == 0 {
		private = nil
	}
	return json.Marshal(persistWire{persistDataAlias: (*persistDataAlias)(&d), Private: private})
}

func (d *persistData) UnmarshalJSON(raw []byte) error {
	if err := uniqueJSONKeys(raw); err != nil {
		return err
	}
	if err := validateTypedJSONKeys(raw, reflect.TypeOf(persistWire{})); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for key := range fields {
		if strings.EqualFold(key, "entity_bson_overlay") && key != "entity_bson_overlay" {
			return errors.New("noncanonical entity overlay field")
		}
	}
	if err := validateHistorySnapshotFields(raw); err != nil {
		return err
	}
	if err := uniqueTagSnapshotFields(raw); err != nil {
		return err
	}
	if _, present := fields["vm_tags"]; present {
		return errors.New("legacy unversioned vm_tags requires an explicit offline conversion")
	}
	if value, present := fields["vm_tag_state"]; present && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return errors.New("VM tag state must be absent or a versioned object, not null")
	}

	if value, ok := fields["entity_bson_overlay"]; ok && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return errors.New("null entity overlay")
	}

	// Decode into a fresh value so absent legacy fields cannot retain stale
	// credentials if a caller reuses a destination struct.
	var decoded persistData
	wire := persistWire{persistDataAlias: (*persistDataAlias)(&decoded)}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	if p := wire.Private; p != nil {
		if decoded.DiscordGateway != nil {
			decoded.DiscordGateway.Token = p.GatewayToken
		}
		for id, private := range p.Enrollments {
			if entry := decoded.PendingEnrollments[id]; entry != nil {
				entry.PollTokenHash = private.PollTokenHash
				entry.IssuedCertPEM = private.IssuedCertPEM
				entry.IssuedCAPEM = private.IssuedCAPEM
			}
		}
		for id, expiry := range p.CommandExpiry {
			if entry := decoded.CommandLog[id]; entry != nil {
				entry.ExpiresAt = expiry
			}
		}
	}
	overlay, err := reconcileOverlay(&decoded, decoded.EntityOverlay)
	if err != nil {
		return err
	}
	decoded.EntityOverlay = overlay
	*d = decoded
	return nil
}
