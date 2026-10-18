// Package growprotocol defines correlated grow intent and guest-reported outcomes.
// It supplies no native device discovery or filesystem implementation.
package growprotocol

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"
)

var ErrRefused = errors.New("unsupported, conflicting or recovery-fenced grow protocol")
var uuid = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var name = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var token = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Target struct {
	VMUUID         string `json:"vm_uuid"`
	DiskSerial     string `json:"disk_serial"`
	FilesystemUUID string `json:"filesystem_uuid"`
	FilesystemType string `json:"filesystem_type"`
	Layout         string `json:"layout"`
}
type Request struct {
	Version      int    `json:"version"`
	ID           string `json:"request_id"`
	Session      string `json:"guest_session"`
	Target       Target `json:"target"`
	DesiredBytes uint64 `json:"desired_bytes"`
}
type Outcome struct {
	Version        int    `json:"version"`
	ID             string `json:"request_id"`
	Session        string `json:"guest_session"`
	Digest         string `json:"request_digest"`
	Status         string `json:"status"`
	ObservedBytes  uint64 `json:"observed_bytes"`
	NativeVerified bool   `json:"native_verified"`
}
type Report struct {
	Version  int       `json:"version"`
	Session  string    `json:"guest_session"`
	Outcomes []Outcome `json:"outcomes"`
}
type Record struct {
	Request Request  `json:"request"`
	Phase   string   `json:"phase"`
	Outcome *Outcome `json:"outcome"`
}
type State struct {
	Version int      `json:"version"`
	Records []Record `json:"records"`
}

func Session() (string, error) {
	b := make([]byte, 32)
	_, e := rand.Read(b)
	return hex.EncodeToString(b), e
}
func (r Request) Validate() error {
	if r.Version != 1 || !name.MatchString(r.ID) || !token.MatchString(r.Session) || !uuid.MatchString(r.Target.VMUUID) || !name.MatchString(r.Target.DiskSerial) || !uuid.MatchString(r.Target.FilesystemUUID) || (r.Target.FilesystemType != "ext4" && r.Target.FilesystemType != "xfs") || r.Target.Layout != "whole-disk" || r.DesiredBytes == 0 || r.DesiredBytes > 1<<60 || r.DesiredBytes%4096 != 0 {
		return ErrRefused
	}
	return nil
}
func (r Request) Digest() string {
	b, _ := json.Marshal(r)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func Overlap(a, b Target) bool {
	return a.VMUUID == b.VMUUID && (a.DiskSerial == b.DiskSerial || a.FilesystemUUID == b.FilesystemUUID)
}
func Result(r Request, status string, size uint64) Outcome {
	return Outcome{Version: 1, ID: r.ID, Session: r.Session, Digest: r.Digest(), Status: status, ObservedBytes: size}
}
func (o Outcome) Validate() error {
	if o.Version != 1 || !name.MatchString(o.ID) || !token.MatchString(o.Session) || !token.MatchString(o.Digest) || o.NativeVerified {
		return ErrRefused
	}
	switch o.Status {
	case "refused_native_unqualified", "refused_identity", "recovery_required":
		if o.ObservedBytes != 0 {
			return ErrRefused
		}
	case "adapter_reported":
		if o.ObservedBytes == 0 || o.ObservedBytes > 1<<60 {
			return ErrRefused
		}
	default:
		return ErrRefused
	}
	return nil
}
func (o Outcome) Matches(r Request) bool {
	return o.Validate() == nil && r.Validate() == nil && o.ID == r.ID && o.Session == r.Session && o.Digest == r.Digest() && (o.Status != "adapter_reported" || o.ObservedBytes >= r.DesiredBytes)
}
func (p Report) Validate() error {
	if p.Version != 1 || !token.MatchString(p.Session) {
		return ErrRefused
	}
	if p.Outcomes == nil || len(p.Outcomes) > 256 {
		return ErrRefused
	}
	seen := map[string]bool{}
	for _, o := range p.Outcomes {
		if o.Validate() != nil || seen[o.ID] {
			return ErrRefused
		}
		seen[o.ID] = true
	}
	return nil
}
func (s *State) Clone() *State {
	if s == nil {
		return &State{Version: 1, Records: []Record{}}
	}
	out := *s
	out.Records = append([]Record{}, s.Records...)
	for i := range out.Records {
		if out.Records[i].Outcome != nil {
			o := *out.Records[i].Outcome
			out.Records[i].Outcome = &o
		}
	}
	return &out
}
func (s *State) Validate() error {
	if s == nil {
		return nil
	}
	if s.Version != 1 || s.Records == nil || len(s.Records) > 256 {
		return ErrRefused
	}
	for i, r := range s.Records {
		if r.Request.Validate() != nil {
			return ErrRefused
		}
		for _, old := range s.Records[:i] {
			if old.Request.ID == r.Request.ID || Overlap(old.Request.Target, r.Request.Target) {
				return ErrRefused
			}
		}
		switch r.Phase {
		case "queued", "offered", "reserved":
			if r.Outcome != nil {
				return ErrRefused
			}
		case "reported":
			if r.Outcome == nil || !r.Outcome.Matches(r.Request) {
				return ErrRefused
			}
		default:
			return ErrRefused
		}
	}
	b, e := json.Marshal((*stateWire)(s))
	if e != nil || len(b) > 1<<20 {
		return ErrRefused
	}
	return nil
}
func Transition(old, next *State) error {
	if old.Validate() != nil || next.Validate() != nil {
		return ErrRefused
	}
	if old == nil {
		return nil
	}
	if next == nil || len(next.Records) < len(old.Records) {
		return ErrRefused
	}
	for i, r := range old.Records {
		n := next.Records[i]
		if r.Request != n.Request {
			return ErrRefused
		}
		if r.Phase == "reported" {
			if !reflect.DeepEqual(r, n) {
				return ErrRefused
			}
		} else if n.Phase != r.Phase && !((r.Phase == "queued" && n.Phase == "offered") || ((r.Phase == "offered" || r.Phase == "reserved") && n.Phase == "reported")) {
			return ErrRefused
		}
	}
	return nil
}

// Strict requires every declared field, rejects aliases/duplicates/unknown fields
// and refuses null in place of a concrete value. No normalization is implicit.
func UniqueJSON(raw []byte) error {
	if !utf8.Valid(raw) || len(raw) > 8<<20 {
		return ErrRefused
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	var walk func() error
	walk = func() error {
		t, e := d.Token()
		if e != nil {
			return e
		}
		if delim, ok := t.(json.Delim); ok {
			switch delim {
			case '{':
				seen := map[string]bool{}
				for d.More() {
					k, e := d.Token()
					if e != nil {
						return e
					}
					key, ok := k.(string)
					if !ok || seen[key] {
						return ErrRefused
					}
					seen[key] = true
					if e = walk(); e != nil {
						return e
					}
				}
			case '[':
				for d.More() {
					if e = walk(); e != nil {
						return e
					}
				}
			default:
				return ErrRefused
			}
			_, e = d.Token()
			return e
		}
		return nil
	}
	if walk() != nil {
		return ErrRefused
	}
	if _, e := d.Token(); e != io.EOF {
		return ErrRefused
	}
	return nil
}

// Envelope permits unrelated legacy telemetry fields but refuses ambiguous v1
// protocol keys, duplicate JSON fields, null protocol values and trailing data.
func Envelope(raw []byte, key string) error {
	if UniqueJSON(raw) != nil {
		return ErrRefused
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return ErrRefused
	}
	for k, v := range fields {
		if strings.EqualFold(k, key) && (k != key || bytes.Equal(bytes.TrimSpace(v), []byte("null"))) {
			return ErrRefused
		}
	}
	return nil
}
func Strict(raw []byte, dst any) error {
	if UniqueJSON(raw) != nil {
		return ErrRefused
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil {
		return ErrRefused
	}
	b, e := json.Marshal(dst)
	if e != nil {
		return ErrRefused
	}
	var a, c any
	if json.Unmarshal(raw, &a) != nil || json.Unmarshal(b, &c) != nil || !reflect.DeepEqual(a, c) {
		return ErrRefused
	}
	return nil
}

type requestWire Request
type outcomeWire Outcome
type reportWire Report
type stateWire State

func (r *Request) UnmarshalJSON(b []byte) error {
	var w requestWire
	if Strict(b, &w) != nil {
		return ErrRefused
	}
	n := Request(w)
	if n.Validate() != nil {
		return ErrRefused
	}
	*r = n
	return nil
}
func (o *Outcome) UnmarshalJSON(b []byte) error {
	var w outcomeWire
	if Strict(b, &w) != nil {
		return ErrRefused
	}
	n := Outcome(w)
	if n.Validate() != nil {
		return ErrRefused
	}
	*o = n
	return nil
}
func (p *Report) UnmarshalJSON(b []byte) error {
	var w reportWire
	if Strict(b, &w) != nil {
		return ErrRefused
	}
	n := Report(w)
	if n.Validate() != nil {
		return ErrRefused
	}
	*p = n
	return nil
}
func (s *State) UnmarshalJSON(b []byte) error {
	var w stateWire
	if Strict(b, &w) != nil {
		return ErrRefused
	}
	n := State(w)
	if n.Validate() != nil {
		return ErrRefused
	}
	*s = n
	return nil
}
