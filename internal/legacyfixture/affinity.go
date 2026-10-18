package legacyfixture

import "go.mongodb.org/mongo-driver/bson"

const AffinityVM2 = "22222222-2222-4222-8222-222222222222"
const AffinityVM3 = "33333333-3333-4333-8333-333333333333"

// GenerateAffinity is a bounded UUID-selector component, never a filtered full fixture.
func GenerateAffinity() Bundle {
	var raw []byte
	for _, r := range []AffinityRule{
		{ID: "synthetic-separate", Name: "Separate replicas", Enabled: true, Kind: "separate", Strength: "must", VMs: []string{VMID, AffinityVM2}},
		{ID: "synthetic-preference", Name: "Preferred neighbours", Enabled: true, Kind: "together", Strength: "should", VMs: []string{VMID, AffinityVM3}},
	} {
		raw = append(raw, marshal(document(r.ID, r))...)
	}
	index := marshal(bson.D{{Key: "v", Value: int32(2)}, {Key: "name", Value: "_id_"}, {Key: "key", Value: bson.D{{Key: "_id", Value: int32(1)}}}})
	return Bundle{Schema: "synthetic-legacy-affinity-6db8c723-v1", Source: DeployedSource, Documents: map[string][]byte{"affinity_rules": raw}, Indexes: map[string][]byte{"affinity_rules": index}, Checksums: map[string]string{"affinity_rules.bson": hash(raw), "affinity_rules.indexes.bson": hash(index)}}
}
