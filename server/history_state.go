package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/bson"
)

const historyVersion = 1
const historyMaxHosts = 64
const historyMaxPoints = 576
const historyBucketSeconds int64 = 300
const historyMaxBytes = 8 << 20
const historyScale uint64 = 10000

// Fixed-point percentages (1 unit = 0.0001%). Sums retain exact sample weights;
// averages are computed only on read. At most one accepted sample per second.
type historyBucket struct {
	T          int64  `json:"t" bson:"t"`
	N          uint32 `json:"n" bson:"n"`
	CPU        uint64 `json:"cpu_sum" bson:"cpu_sum"`
	CPUMax     uint64 `json:"cpu_max" bson:"cpu_max"`
	Mem        uint64 `json:"mem_sum" bson:"mem_sum"`
	MemMax     uint64 `json:"mem_max" bson:"mem_max"`
	Storage    uint64 `json:"storage_sum" bson:"storage_sum"`
	StorageMax uint64 `json:"storage_max" bson:"storage_max"`
	VMs        int    `json:"vms" bson:"vms"`
}
type hostHistory struct {
	Last   int64           `json:"last_sample" bson:"last_sample"`
	Points []historyBucket `json:"points" bson:"points"`
}
type historyState struct {
	Version int                    `json:"version" bson:"version"`
	Hosts   map[string]hostHistory `json:"hosts" bson:"hosts"`
}
type historyWire historyState

func validHistoryID(id string) bool {
	return id != "" && len(id) <= 255 && utf8.ValidString(id) && !strings.ContainsAny(id, "\x00/\r\n\t") && strings.TrimSpace(id) == id
}
func (d *historyState) validate() error {
	if d == nil {
		return nil
	}
	if d.Version != historyVersion || d.Hosts == nil || len(d.Hosts) > historyMaxHosts {
		return errors.New("unsupported or oversized host history state")
	}
	for id, h := range d.Hosts {
		if !validHistoryID(id) || len(h.Points) == 0 || len(h.Points) > historyMaxPoints {
			return errors.New("invalid host history identity or point count")
		}
		var previous int64
		for _, p := range h.Points {
			if p.T < 946684800 || p.T > 253402300500 || p.T%historyBucketSeconds != 0 || p.T <= previous || p.N == 0 || p.N > 300 || p.VMs < 0 || p.VMs > 1000000 {
				return errors.New("invalid host history bucket")
			}
			for _, pair := range [][2]uint64{{p.CPU, p.CPUMax}, {p.Mem, p.MemMax}, {p.Storage, p.StorageMax}} {
				if pair[1] > 100*historyScale || pair[0] < pair[1] || pair[0] > pair[1]*uint64(p.N) {
					return errors.New("invalid host history percentage aggregate")
				}
			}
			previous = p.T
		}
		if h.Last < previous || h.Last >= previous+historyBucketSeconds || int64(h.Points[len(h.Points)-1].N) > h.Last-previous+1 {
			return errors.New("invalid host history last sample")
		}
	}
	raw, err := json.Marshal(historyWire(*d))
	if err != nil {
		return err
	}
	if len(raw) > historyMaxBytes {
		return errors.New("host history exceeds encoded size limit")
	}
	return nil
}
func (d historyState) MarshalJSON() ([]byte, error) {
	if err := d.validate(); err != nil {
		return nil, err
	}
	return json.Marshal(historyWire(d))
}
func historyObject(raw []byte, keys ...string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if fields == nil || len(fields) != len(keys) {
		return nil, errors.New("invalid host history object fields")
	}
	for _, key := range keys {
		if value, ok := fields[key]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, errors.New("unknown or missing host history field")
		}
	}
	return fields, nil
}
func (d *historyState) UnmarshalJSON(raw []byte) error {
	if len(raw) > 4*historyMaxBytes {
		return errors.New("host history input exceeds size limit")
	}
	if err := uniqueJSONKeys(raw); err != nil {
		return fmt.Errorf("host history: %w", err)
	}
	fields, err := historyObject(raw, "version", "hosts")
	if err != nil {
		return err
	}
	var hosts map[string]json.RawMessage
	if err = json.Unmarshal(fields["hosts"], &hosts); err != nil {
		return err
	}
	if len(hosts) > historyMaxHosts {
		return errors.New("too many history hosts")
	}
	for _, host := range hosts {
		h, err := historyObject(host, "last_sample", "points")
		if err != nil {
			return err
		}
		var points []json.RawMessage
		if err = json.Unmarshal(h["points"], &points); err != nil {
			return err
		}
		if len(points) > historyMaxPoints {
			return errors.New("too many history buckets")
		}
		for _, point := range points {
			if _, err := historyObject(point, "t", "n", "cpu_sum", "cpu_max", "mem_sum", "mem_max", "storage_sum", "storage_max", "vms"); err != nil {
				return err
			}
		}
	}
	var decoded historyWire
	if err = json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	next := historyState(decoded)
	if err = next.validate(); err != nil {
		return err
	}
	*d = next
	return nil
}

// BSON maps are converted only after recursively rejecting duplicate keys.
// JSON validation then applies exact fields and integral numeric values. BSON
// integral doubles are semantically accepted; fractional numbers are rejected.
func uniqueHistoryBSON(raw bson.Raw) error {
	if err := uniqueBSONKeys(raw); err != nil {
		return err
	}
	elements, err := raw.Elements()
	if err != nil {
		return err
	}
	for _, e := range elements {
		v := e.Value()
		if v.Type == bson.TypeEmbeddedDocument {
			if err := uniqueHistoryBSON(v.Document()); err != nil {
				return err
			}
		}
		if v.Type == bson.TypeArray {
			values, err := v.Array().Values()
			if err != nil {
				return err
			}
			for _, item := range values {
				if item.Type == bson.TypeEmbeddedDocument {
					if err := uniqueHistoryBSON(item.Document()); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}
func (d *historyState) UnmarshalBSON(raw []byte) error {
	if len(raw) > historyMaxBytes {
		return errors.New("host history BSON exceeds size limit")
	}
	if err := uniqueHistoryBSON(raw); err != nil {
		return err
	}
	var doc bson.M
	if err := bson.Unmarshal(raw, &doc); err != nil {
		return err
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	return d.UnmarshalJSON(data)
}
func cloneHistory(in *historyState) *historyState {
	if in == nil {
		return nil
	}
	out := &historyState{Version: in.Version, Hosts: make(map[string]hostHistory, len(in.Hosts))}
	for id, h := range in.Hosts {
		h.Points = append([]historyBucket(nil), h.Points...)
		out.Hosts[id] = h
	}
	return out
}
func historyCutoff(now int64) int64 {
	return now - now%historyBucketSeconds - (historyMaxPoints-1)*historyBucketSeconds
}
func pruneHistory(d *historyState, now int64) {
	if d == nil {
		return
	}
	cut := historyCutoff(now)
	for id, h := range d.Hosts {
		i := 0
		for i < len(h.Points) && h.Points[i].T < cut {
			i++
		}
		if i == len(h.Points) {
			delete(d.Hosts, id)
		} else if i > 0 {
			h.Points = append([]historyBucket(nil), h.Points[i:]...)
			d.Hosts[id] = h
		}
	}
}
func (s *Store) historyStateLocked() *historyState {
	if s.history == nil || len(s.history.Hosts) == 0 {
		return nil
	}
	return s.history
}
func historyValue(v float64) (uint64, bool) {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 100 {
		return 0, false
	}
	return uint64(math.Round(v * float64(historyScale))), true
}
func (s *Store) recordHistory(id string, observed time.Time, cpu, mem, storage float64, vms int) bool {
	at := observed.Unix()
	c, okC := historyValue(cpu)
	m, okM := historyValue(mem)
	st, okS := historyValue(storage)
	if !validHistoryID(id) || at < 946684800 || at > 253402300799 || !okC || !okM || !okS || vms < 0 || vms > 1000000 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.historyEnabled {
		return false
	}
	if s.history == nil {
		s.history = &historyState{Version: historyVersion, Hosts: map[string]hostHistory{}}
	}
	// Use current server time for expiry, never a delayed observation timestamp.
	pruneHistory(s.history, time.Now().Unix())
	if at < historyCutoff(time.Now().Unix()) {
		return false
	}
	h, exists := s.history.Hosts[id]
	if (!exists && len(s.history.Hosts) >= historyMaxHosts) || at <= h.Last {
		return false
	}
	start := at - at%historyBucketSeconds
	if len(h.Points) == 0 || h.Points[len(h.Points)-1].T != start {
		h.Points = append(h.Points, historyBucket{T: start})
	}
	p := &h.Points[len(h.Points)-1]
	if p.N >= 300 {
		return false
	}
	p.N++
	p.CPU += c
	p.Mem += m
	p.Storage += st
	p.CPUMax = max(p.CPUMax, c)
	p.MemMax = max(p.MemMax, m)
	p.StorageMax = max(p.StorageMax, st)
	p.VMs = vms
	if len(h.Points) > historyMaxPoints {
		h.Points = append([]historyBucket(nil), h.Points[len(h.Points)-historyMaxPoints:]...)
	}
	h.Last = at
	s.history.Hosts[id] = h
	return true
}

// Reject legacy formats and case aliases before encoding/json's case folding.
func validateHistorySnapshotFields(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if _, err := decoder.Token(); err != nil {
		return err
	}
	seen := false
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return err
		}
		name, ok := key.(string)
		if !ok {
			return errors.New("invalid snapshot object")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		if strings.EqualFold(name, "history") {
			return errors.New("legacy history requires explicit offline conversion")
		}
		if strings.EqualFold(name, "host_history_state") {
			if name != "host_history_state" || seen || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return errors.New("invalid host history snapshot field")
			}
			seen = true
		}
	}
	return nil
}
