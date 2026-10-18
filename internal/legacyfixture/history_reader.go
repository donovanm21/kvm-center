package legacyfixture

import "go.mongodb.org/mongo-driver/bson"

// GenerateHistoryReader is a separate bounded component fixture, never a
// filtered full-schema fixture. Three raw samples at +0,+60,+300 seconds fold
// independently into each pinned tier; VMs records the final sample's count.
func GenerateHistoryReader() Bundle {
	first := historyPoint{T: 1704067200, CPUAvg: 20, CPUMax: 30, MemAvg: 30, MemMax: 40, StorageAvg: 40, StorageMax: 50, VMs: 3, N: 2}
	second := historyPoint{T: 1704067500, CPUAvg: 50, CPUMax: 50, MemAvg: 60, MemMax: 60, StorageAvg: 70, StorageMax: 70, VMs: 4, N: 1}
	coarse := historyPoint{T: 1704067200, CPUAvg: 30, CPUMax: 50, MemAvg: 40, MemMax: 60, StorageAvg: 50, StorageMax: 70, VMs: 4, N: 3}
	raw := marshal(bson.D{{Key: "_id", Value: HostID}, {Key: "host", Value: HostID}, {Key: "tiers", Value: bson.D{{Key: "5m", Value: []historyPoint{first, second}}, {Key: "1h", Value: []historyPoint{coarse}}, {Key: "1d", Value: []historyPoint{coarse}}}}})
	index := marshal(bson.D{{Key: "v", Value: int32(2)}, {Key: "name", Value: "_id_"}, {Key: "key", Value: bson.D{{Key: "_id", Value: int32(1)}}}})
	return Bundle{Schema: "synthetic-legacy-history-reader-v1", Source: DeployedSource, Documents: map[string][]byte{"perf_history": raw}, Indexes: map[string][]byte{"perf_history": index}, Checksums: map[string]string{"perf_history.bson": hash(raw), "perf_history.indexes.bson": hash(index)}}
}
