package offlinestaging

import (
	"go.mongodb.org/mongo-driver/bson"
	"math"
	"testing"
)

func legacyPoint() bson.D {
	return bson.D{{Key: "t", Value: int64(1704067200)}, {Key: "cpu_avg", Value: 12.5}, {Key: "cpu_max", Value: 30.0}, {Key: "mem_avg", Value: 20.0}, {Key: "mem_max", Value: 40.0}, {Key: "storage_avg", Value: 40.0}, {Key: "storage_max", Value: 60.0}, {Key: "vms", Value: int32(3)}, {Key: "n", Value: int32(7)}}
}
func legacyRecord(point bson.D) bson.D {
	return bson.D{{Key: "_id", Value: "synthetic-host"}, {Key: "host", Value: "synthetic-host"}, {Key: "tiers", Value: bson.D{{Key: "5m", Value: bson.A{point}}}}}
}
func setLegacyField(d bson.D, key string, value any) bson.D {
	for i := range d {
		if d[i].Key == key {
			d[i].Value = value
			return d
		}
	}
	return append(d, bson.E{Key: key, Value: value})
}
func TestLegacyHistoryStrictSemantics(t *testing.T) {
	good := raw(t, legacyRecord(legacyPoint()))
	got, err := decodeLegacyHistory(good)
	if err != nil || got.Tiers[0].Points[0].N != 7 || got.Tiers[0].Points[0].VMs != 3 || got.Tiers[0].Points[0].CPUAvg != 12.5 || got.OriginalSHA256 != digest(good) || got.ActiveCompatible {
		t.Fatal("aggregate semantics", err)
	}
	for _, key := range []string{"n", "vms"} {
		p := setLegacyField(legacyPoint(), key, int64(9))
		if _, err := decodeLegacyHistory(raw(t, legacyRecord(p))); err != nil {
			t.Fatal("int64 count", err)
		}
	}
	for name, change := range map[string]func() bson.D{
		"unknown-top": func() bson.D { return append(legacyRecord(legacyPoint()), bson.E{Key: "opaque", Value: 1}) },
		"identity":    func() bson.D { return setLegacyField(legacyRecord(legacyPoint()), "host", "other") },
		"alias":       func() bson.D { return setLegacyField(legacyRecord(legacyPoint()), "Host", "synthetic-host") },
		"unknown-tier": func() bson.D {
			return setLegacyField(legacyRecord(legacyPoint()), "tiers", bson.D{{Key: "5m", Value: bson.A{legacyPoint()}}, {Key: "future", Value: bson.A{}}})
		},
		"null-tier": func() bson.D {
			return setLegacyField(legacyRecord(legacyPoint()), "tiers", bson.D{{Key: "5m", Value: nil}})
		},
		"null-point": func() bson.D {
			return setLegacyField(legacyRecord(legacyPoint()), "tiers", bson.D{{Key: "5m", Value: bson.A{nil}}})
		},
		"duplicate-tier": func() bson.D {
			return setLegacyField(legacyRecord(legacyPoint()), "tiers", bson.D{{Key: "5m", Value: bson.A{}}, {Key: "5m", Value: bson.A{}}})
		},
		"duplicate-point": func() bson.D { return legacyRecord(append(legacyPoint(), bson.E{Key: "n", Value: 3})) },
		"order": func() bson.D {
			return setLegacyField(legacyRecord(legacyPoint()), "tiers", bson.D{{Key: "5m", Value: bson.A{legacyPoint(), legacyPoint()}}})
		},
		"over-bound": func() bson.D {
			a := bson.A{}
			for i := 0; i < 577; i++ {
				a = append(a, legacyPoint())
			}
			return setLegacyField(legacyRecord(legacyPoint()), "tiers", bson.D{{Key: "5m", Value: a}})
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeLegacyHistory(raw(t, change())); err == nil {
				t.Fatal("unsupported record accepted")
			}
		})
	}
	for _, bad := range []struct {
		key   string
		value any
	}{{"n", 0}, {"n", 7.0}, {"vms", -1}, {"vms", "3"}, {"t", int32(1704067200)}, {"t", int64(1704067201)}, {"t", int64(0)}, {"cpu_avg", int32(12)}, {"cpu_max", math.Inf(1)}, {"mem_avg", math.NaN()}, {"storage_max", 101.0}, {"storage_avg", -1.0}, {"cpuAvg", 12.5}} {
		if _, err := decodeLegacyHistory(raw(t, legacyRecord(setLegacyField(legacyPoint(), bad.key, bad.value)))); err == nil {
			t.Fatalf("accepted %s", bad.key)
		}
	}
}

func TestLegacyHistoryPreservesPinnedFloatingRounding(t *testing.T) {
	avg := 0.1
	for n := 1.0; n < 3; n++ {
		avg = (avg*n + 0.1) / (n + 1)
	}
	p := setLegacyField(legacyPoint(), "cpu_avg", avg)
	p = setLegacyField(p, "cpu_max", 0.1)
	view, err := decodeLegacyHistory(raw(t, legacyRecord(p)))
	if err != nil || view.Tiers[0].Points[0].CPUAvg != avg || view.Tiers[0].Points[0].CPUMax != 0.1 {
		t.Fatal("pinned arithmetic changed", err)
	}
}
