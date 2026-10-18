package offlinestaging

import (
	"encoding/json"
	"errors"
	"go.mongodb.org/mongo-driver/bson"
	"strings"
	"unicode/utf8"
)

// LegacyHistoryState is an immutable archive alongside new application history.
// Bounds refuse admission; they never truncate or expire the retained originals.
type LegacyHistoryState struct {
	Version        int               `json:"version"`
	SourceContract string            `json:"source_contract"`
	Records        map[string][]byte `json:"records"`
}
type legacyHistoryStateWire LegacyHistoryState

const LegacyHistoryMaxHosts = 64
const LegacyHistoryMaxBytes = 2 << 20

func (s *LegacyHistoryState) Validate() error {
	if s == nil {
		return nil
	}
	if s.Version != 1 || s.SourceContract != LegacyHistorySource || s.Records == nil || len(s.Records) == 0 || len(s.Records) > LegacyHistoryMaxHosts {
		return errors.New("unsupported legacy history state contract/bound")
	}
	total := 0
	for id, raw := range s.Records {
		total += len(raw)
		if total > LegacyHistoryMaxBytes {
			return errors.New("legacy history state exceeds byte bound")
		}
		if id == "" || len(id) > 255 || !utf8.ValidString(id) || strings.ContainsAny(id, "\x00/\r\n\t") || strings.TrimSpace(id) != id {
			return errors.New("invalid legacy history state host identity")
		}
		v, err := decodeLegacyHistory(bson.Raw(raw))
		if err != nil {
			return err
		}
		if v.Host != id {
			return errors.New("legacy history state identity mismatch")
		}
	}
	return nil
}
func (s LegacyHistoryState) MarshalJSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(legacyHistoryStateWire(s))
}
func (s *LegacyHistoryState) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := decodeKeys(raw, &fields, false); err != nil {
		return err
	}
	if len(fields) != 3 {
		return errors.New("unsupported legacy history state fields")
	}
	for _, key := range []string{"version", "source_contract", "records"} {
		if _, ok := fields[key]; !ok {
			return errors.New("missing legacy history state field")
		}
	}
	var w legacyHistoryStateWire
	if err := decodeKeys(raw, &w, false); err != nil {
		return err
	}
	n := LegacyHistoryState(w)
	if err := n.Validate(); err != nil {
		return err
	}
	*s = n
	return nil
}
func (s *LegacyHistoryState) Clone() *LegacyHistoryState {
	if s == nil {
		return nil
	}
	n := &LegacyHistoryState{Version: s.Version, SourceContract: s.SourceContract, Records: map[string][]byte{}}
	for id, raw := range s.Records {
		n.Records[id] = append([]byte(nil), raw...)
	}
	return n
}
func (s *LegacyHistoryState) Digest() (string, error) {
	if s == nil {
		return "", nil
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	return digest(raw), nil
}
func (s *LegacyHistoryState) View(id string) (LegacyHistoryView, error) {
	if s == nil {
		return LegacyHistoryView{}, errors.New("legacy history absent")
	}
	if err := s.Validate(); err != nil {
		return LegacyHistoryView{}, err
	}
	return decodeLegacyHistory(bson.Raw(s.Records[id]))
}

// ExportLegacyHistoryState is the explicit conversion from a verified pinned
// synthetic bundle. Both raw records and interpretation must validate first.
func ExportLegacyHistoryState(path string) (*LegacyHistoryState, error) {
	_, s, err := verifySnapshot(path)
	if err != nil {
		return nil, err
	}
	if s.manifest.SourceBuild != LegacyHistorySource {
		return nil, errors.New("legacy history requires the pinned source contract")
	}
	if docs, err := documents(s.files["host_history_state.bson"]); err != nil || len(docs) != 0 {
		return nil, errors.New("mixed or current history semantics refused")
	}
	docs, err := documents(s.files["perf_history.bson"])
	if err != nil {
		return nil, err
	}
	state := &LegacyHistoryState{Version: 1, SourceContract: LegacyHistorySource, Records: map[string][]byte{}}
	for _, raw := range docs {
		view, err := decodeLegacyHistory(raw)
		if err != nil {
			return nil, err
		}
		state.Records[view.Host] = append([]byte(nil), raw...)
	}
	if err := state.Validate(); err != nil {
		return nil, err
	}
	return state, nil
}
