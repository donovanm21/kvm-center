package offlinestaging

import (
	"errors"
	"math"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/bsontype"
)

// LegacyHistorySource pins the writer and aggregation semantics, not a claim
// that a supplied snapshot came from a particular deployed executable.
const LegacyHistorySource = "6db8c7237bd7cf507532391c0dc0c2e36a70f696"
const LegacyHistorySchema = "legacy-history-6db8c723-reader-v1"

// LegacyHistoryPoint retains all stored aggregate values. N counts raw samples;
// VMs is the last sample's VM count, not an average or maximum.
type LegacyHistoryPoint struct {
	T          int64   `json:"t"`
	CPUAvg     float64 `json:"cpu_avg"`
	CPUMax     float64 `json:"cpu_max"`
	MemAvg     float64 `json:"mem_avg"`
	MemMax     float64 `json:"mem_max"`
	StorageAvg float64 `json:"storage_avg"`
	StorageMax float64 `json:"storage_max"`
	VMs        int64   `json:"vms"`
	N          int64   `json:"n"`
}
type LegacyHistoryTier struct {
	Name               string               `json:"name"`
	BucketSeconds      int64                `json:"bucket_seconds"`
	RetainedPointLimit int                  `json:"retained_point_limit"`
	Points             []LegacyHistoryPoint `json:"points"`
}
type LegacyHistoryView struct {
	Schema           string              `json:"schema"`
	SourceContract   string              `json:"source_contract"`
	Host             string              `json:"host"`
	OriginalSHA256   string              `json:"original_sha256"`
	TimestampUnit    string              `json:"timestamp_unit"`
	MetricUnit       string              `json:"metric_unit"`
	VMCountMeaning   string              `json:"vm_count_meaning"`
	RetentionMeaning string              `json:"retention_meaning"`
	ActiveCompatible bool                `json:"active_compatible"`
	Tiers            []LegacyHistoryTier `json:"tiers"`
}

// LegacyHistory reads a fully verified snapshot and refuses any unsupported
// history representation before returning any results. It never rolls up,
// truncates, expires or rewrites records. Use History for uninterpreted originals.
func LegacyHistory(path string) ([]LegacyHistoryView, error) {
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
	out := make([]LegacyHistoryView, 0, len(docs))
	for _, raw := range docs {
		view, err := decodeLegacyHistory(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, view)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out, nil
}

func exactHistoryFields(raw bson.Raw, names ...string) error {
	if err := validateRaw(raw, 0, false); err != nil {
		return err
	}
	elements, err := raw.Elements()
	if err != nil || len(elements) != len(names) {
		return errors.New("unsupported legacy history fields")
	}
	allowed := map[string]bool{}
	for _, name := range names {
		allowed[name] = true
	}
	for _, e := range elements {
		if !allowed[e.Key()] {
			return errors.New("unsupported legacy history field or alias")
		}
	}
	return nil
}
func historyInteger(v bson.RawValue) (int64, error) {
	switch v.Type {
	case bsontype.Int32:
		return int64(v.Int32()), nil
	case bsontype.Int64:
		return v.Int64(), nil
	default:
		return 0, errors.New("legacy history count must be a BSON integer")
	}
}
func decodeLegacyHistory(raw bson.Raw) (LegacyHistoryView, error) {
	var out LegacyHistoryView
	if err := exactHistoryFields(raw, "_id", "host", "tiers"); err != nil {
		return out, err
	}
	host, ok := raw.Lookup("host").StringValueOK()
	id, idOK := raw.Lookup("_id").StringValueOK()
	if !ok || !idOK || host == "" || host != id || strings.TrimSpace(host) != host {
		return out, errors.New("ambiguous legacy history host identity")
	}
	tiers, ok := raw.Lookup("tiers").DocumentOK()
	if !ok {
		return out, errors.New("legacy history tiers must be a document")
	}
	elements, err := tiers.Elements()
	if err != nil {
		return out, err
	}
	bounds := map[string]struct {
		seconds int64
		limit   int
	}{"5m": {300, 576}, "1h": {3600, 720}, "1d": {86400, 365}}
	for _, e := range elements {
		if _, ok := bounds[e.Key()]; !ok {
			return out, errors.New("unsupported legacy history tier")
		}
	}
	out = LegacyHistoryView{Schema: LegacyHistorySchema, SourceContract: LegacyHistorySource, Host: host, OriginalSHA256: digest(raw), TimestampUnit: "unix_seconds_UTC_bucket_start", MetricUnit: "percent", VMCountMeaning: "last_raw_sample", RetentionMeaning: "point_count_per_tier_not_wall_clock_expiry", Tiers: []LegacyHistoryTier{}}
	for _, name := range []string{"5m", "1h", "1d"} {
		value := tiers.Lookup(name)
		if value.Type == 0 {
			continue
		}
		if value.Type != bsontype.Array {
			return LegacyHistoryView{}, errors.New("legacy history tier must be an array")
		}
		values, err := value.Array().Values()
		b := bounds[name]
		if err != nil || len(values) > b.limit {
			return LegacyHistoryView{}, errors.New("legacy history tier exceeds pinned point bound")
		}
		tier := LegacyHistoryTier{Name: name, BucketSeconds: b.seconds, RetainedPointLimit: b.limit, Points: []LegacyHistoryPoint{}}
		var previous int64
		for i, value := range values {
			if value.Type != bsontype.EmbeddedDocument {
				return LegacyHistoryView{}, errors.New("legacy history point must be a document")
			}
			rawPoint := value.Document()
			if err := exactHistoryFields(rawPoint, "t", "cpu_avg", "cpu_max", "mem_avg", "mem_max", "storage_avg", "storage_max", "vms", "n"); err != nil {
				return LegacyHistoryView{}, err
			}
			timestamp, ok := rawPoint.Lookup("t").Int64OK()
			if !ok || timestamp < 946684800 || timestamp%b.seconds != 0 || (i > 0 && timestamp <= previous) {
				return LegacyHistoryView{}, errors.New("unsupported legacy history timestamp/order")
			}
			previous = timestamp
			point := LegacyHistoryPoint{T: timestamp}
			for _, field := range []struct {
				name   string
				target *float64
			}{{"cpu_avg", &point.CPUAvg}, {"cpu_max", &point.CPUMax}, {"mem_avg", &point.MemAvg}, {"mem_max", &point.MemMax}, {"storage_avg", &point.StorageAvg}, {"storage_max", &point.StorageMax}} {
				n, ok := rawPoint.Lookup(field.name).DoubleOK()
				if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 100 {
					return LegacyHistoryView{}, errors.New("unsupported legacy history percentage type/range")
				}
				*field.target = n
			}
			point.N, err = historyInteger(rawPoint.Lookup("n"))
			if err != nil || point.N <= 0 {
				return LegacyHistoryView{}, errors.New("invalid legacy history sample count")
			}
			point.VMs, err = historyInteger(rawPoint.Lookup("vms"))
			if err != nil || point.VMs < 0 {
				return LegacyHistoryView{}, errors.New("invalid legacy history VM count")
			}
			tier.Points = append(tier.Points, point)
		}
		out.Tiers = append(out.Tiers, tier)
	}
	return out, nil
}
