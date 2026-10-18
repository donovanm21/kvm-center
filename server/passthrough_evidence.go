package main

import (
	"context"
	"encoding/binary"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/bsontype"
	"go.mongodb.org/mongo-driver/mongo/options"
	"kvm-center/internal/models"
)

var errPassthroughEvidence = errors.New("unsupported or ambiguous retained passthrough evidence")
var pciEvidenceAddress = regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{2}:[01][0-9a-f]\.[0-7]$`)

type passthroughEvidence struct {
	UUID             string               `json:"uuid"`
	Source           string               `json:"source"`
	Status           string               `json:"status"`
	HasPassthrough   *bool                `json:"has_passthrough"`
	PCIRecordsStatus string               `json:"pci_records_status"`
	PCIDevices       []models.VMPCIDevice `json:"pci_devices"`
	NativeVerified   bool                 `json:"native_verified"`
}

// This projects retained inventory, never an attachment or isolation assertion.
// The legacy flag remains opaque to writable VM models and agent telemetry.
func decodePassthroughEvidence(raw bson.Raw, id string) (*passthroughEvidence, error) {
	if len(raw) < 5 || int(binary.LittleEndian.Uint32(raw[:4])) != len(raw) {
		return nil, errPassthroughEvidence
	}
	fields, err := rawFields(raw)
	if err != nil {
		return nil, errPassthroughEvidence
	}
	got, err := entityIdentity(raw, mcoVMs)
	if err != nil || got != id {
		return nil, errPassthroughEvidence
	}
	for key := range fields {
		switch strings.ToLower(strings.ReplaceAll(key, "_", "")) {
		case "haspassthrough":
			if key != "haspassthrough" {
				return nil, errPassthroughEvidence
			}
		case "pcidevices":
			if key != "pcidevices" {
				return nil, errPassthroughEvidence
			}
		case "uuid":
			if key != "uuid" {
				return nil, errPassthroughEvidence
			}
		case "id":
			if key != "_id" {
				return nil, errPassthroughEvidence
			}
		}
	}
	view := &passthroughEvidence{UUID: id, Source: "persisted_inventory", Status: "unverified_inventory", PCIRecordsStatus: "missing", PCIDevices: []models.VMPCIDevice{}}
	if flag, ok := fields["haspassthrough"]; ok {
		value, valid := flag.BooleanOK()
		if !valid {
			return nil, errPassthroughEvidence
		}
		view.HasPassthrough = &value
	}
	if devices, ok := fields["pcidevices"]; ok {
		if devices.Type == bsontype.Null {
			view.PCIRecordsStatus = "null"
			return view, nil
		}
		array, valid := devices.ArrayOK()
		if !valid {
			return nil, errPassthroughEvidence
		}
		elements, err := array.Elements()
		if err != nil {
			return nil, errPassthroughEvidence
		}
		for i, e := range elements {
			if e.Key() != strconv.Itoa(i) {
				return nil, errPassthroughEvidence
			}
		}
		values, err := array.Values()
		if err != nil || len(values) > 64 {
			return nil, errPassthroughEvidence
		}
		view.PCIRecordsStatus = "present"
		seen := map[string]bool{}
		for _, v := range values {
			doc, valid := v.DocumentOK()
			if !valid || strictBSONKeys(doc, "sourceaddr", "hostdesc") != nil {
				return nil, errPassthroughEvidence
			}
			address, valid := doc.Lookup("sourceaddr").StringValueOK()
			if !valid || !pciEvidenceAddress.MatchString(address) || seen[address] {
				return nil, errPassthroughEvidence
			}
			seen[address] = true
			desc, valid := doc.Lookup("hostdesc").StringValueOK()
			if !valid || len(desc) > 4096 || !utf8.ValidString(desc) {
				return nil, errPassthroughEvidence
			}
			view.PCIDevices = append(view.PCIDevices, models.VMPCIDevice{SourceAddr: address, HostDesc: desc})
		}
	}
	// A false legacy flag with PCI records is contradictory retained evidence.
	// A true flag with no PCI records can represent non-PCI passthrough.
	if view.HasPassthrough != nil && !*view.HasPassthrough && len(view.PCIDevices) > 0 {
		return nil, errPassthroughEvidence
	}
	return view, nil
}

func (s *Store) PassthroughEvidence(ctx context.Context, id string) (*passthroughEvidence, error) {
	if !tagUUIDPattern.MatchString(id) || strings.ToLower(id) != id {
		return nil, errPassthroughEvidence
	}
	if err := s.saveMu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.saveMu.Unlock()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.saveGateOpen || s.backend == nil {
		return nil, errPassthroughEvidence
	}
	count := 0
	for _, vms := range s.vms {
		for _, v := range vms {
			if v != nil && strings.EqualFold(v.UUID, id) {
				count++
			}
		}
	}
	if count != 1 {
		return nil, errPassthroughEvidence
	}
	var raw bson.Raw
	switch b := s.backend.(type) {
	case *MongoBackend:
		db, err := b.dial(ctx)
		if err != nil {
			return nil, err
		}
		// Match both identity fields so a conflicting or case-colliding row cannot
		// be hidden behind a convenient canonical _id lookup.
		filter := bson.M{"$or": bson.A{bson.M{"_id": bson.M{"$regex": "^" + regexp.QuoteMeta(id) + "$", "$options": "i"}}, bson.M{"uuid": bson.M{"$regex": "^" + regexp.QuoteMeta(id) + "$", "$options": "i"}}}}
		cur, err := db.Collection(mcoVMs).Find(ctx, filter, options.Find().SetLimit(2))
		if err != nil {
			return nil, err
		}
		defer cur.Close(ctx)
		for cur.Next(ctx) {
			if raw != nil {
				return nil, errPassthroughEvidence
			}
			raw = append(bson.Raw(nil), cur.Current...)
		}
		if err = cur.Err(); err != nil {
			return nil, err
		}
	case *JSONBackend:
		d, err := b.Load(ctx)
		if err != nil {
			return nil, err
		}
		n := 0
		var vm *models.VM
		for _, vs := range d.VMs {
			for _, v := range vs {
				if v != nil && strings.EqualFold(v.UUID, id) {
					n++
					vm = v
				}
			}
		}
		if n != 1 {
			return nil, errPassthroughEvidence
		}
		if d.EntityOverlay != nil {
			raw = append(bson.Raw(nil), d.EntityOverlay.Collections[mcoVMs][id]...)
		}
		if len(raw) == 0 {
			raw, err = bson.Marshal(struct {
				ID         string `bson:"_id"`
				*models.VM `bson:",inline"`
			}{id, vm})
			if err != nil {
				return nil, err
			}
		}
	default:
		return nil, errPassthroughEvidence
	}
	if len(raw) == 0 {
		return nil, errPassthroughEvidence
	}
	return decodePassthroughEvidence(raw, id)
}

func handlePassthroughEvidence(s *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			writeError(w, 405, "GET only")
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/vms/"), "/passthrough-evidence")
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		view, err := s.PassthroughEvidence(ctx, id)
		if err != nil {
			writeError(w, 409, "retained passthrough evidence unavailable, unsupported or ambiguous")
			return
		}
		writeJSON(w, 200, view)
	}
}
