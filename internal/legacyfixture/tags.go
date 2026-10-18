package legacyfixture

import "go.mongodb.org/mongo-driver/bson"

const OrphanTagVMID = "22222222-2222-4222-8222-222222222222"

// GenerateTags is a separate component fixture matching the saved writer's
// exact {_id,tags} records. It never filters or replaces a full-schema fixture.
func GenerateTags() Bundle {
	raw := marshal(bson.D{{Key: "_id", Value: VMID}, {Key: "tags", Value: []string{"database", "production"}}})
	raw = append(raw, marshal(bson.D{{Key: "_id", Value: OrphanTagVMID}, {Key: "tags", Value: []string{"retained-orphan"}}})...)
	index := marshal(bson.D{{Key: "v", Value: int32(2)}, {Key: "name", Value: "_id_"}, {Key: "key", Value: bson.D{{Key: "_id", Value: int32(1)}}}})
	return Bundle{Schema: "synthetic-legacy-tags-c018b31-v1", Source: Source, Documents: map[string][]byte{"vm_tags": raw}, Indexes: map[string][]byte{"vm_tags": index}, Checksums: map[string]string{"vm_tags.bson": hash(raw), "vm_tags.indexes.bson": hash(index)}}
}
