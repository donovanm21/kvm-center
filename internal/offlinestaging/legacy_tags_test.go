package offlinestaging

import (
	"go.mongodb.org/mongo-driver/bson"
	"testing"
)

func TestLegacyTagStrictSemantics(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	good := func() bson.D {
		return bson.D{{Key: "_id", Value: id}, {Key: "tags", Value: bson.A{"database", "production"}}}
	}
	got, tags, err := decodeLegacyTags(raw(t, good()))
	if err != nil || got != id || len(tags) != 2 {
		t.Fatal(err)
	}
	for name, doc := range map[string]bson.D{
		"extra":           append(good(), bson.E{Key: "opaque", Value: true}),
		"alias":           {{Key: "_id", Value: id}, {Key: "Tags", Value: bson.A{"x"}}},
		"duplicate-field": append(good(), bson.E{Key: "tags", Value: bson.A{"x"}}),
		"identity":        {{Key: "_id", Value: "not-a-uuid"}, {Key: "tags", Value: bson.A{"x"}}},
		"null":            {{Key: "_id", Value: id}, {Key: "tags", Value: nil}},
		"empty":           {{Key: "_id", Value: id}, {Key: "tags", Value: bson.A{}}},
		"unsorted":        {{Key: "_id", Value: id}, {Key: "tags", Value: bson.A{"z", "a"}}},
		"duplicate-tag":   {{Key: "_id", Value: id}, {Key: "tags", Value: bson.A{"a", "a"}}},
		"case":            {{Key: "_id", Value: id}, {Key: "tags", Value: bson.A{"Production"}}},
		"space":           {{Key: "_id", Value: id}, {Key: "tags", Value: bson.A{" production"}}},
		"type":            {{Key: "_id", Value: id}, {Key: "tags", Value: bson.A{int32(1)}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := decodeLegacyTags(raw(t, doc)); err == nil {
				t.Fatal("unsupported semantics admitted")
			}
		})
	}
	index := bson.D{{Key: "v", Value: int32(2)}, {Key: "name", Value: "_id_"}, {Key: "key", Value: bson.D{{Key: "_id", Value: int32(1)}}}}
	if validateLegacyTagIndexes(raw(t, index)) != nil {
		t.Fatal("identity index refused")
	}
	if validateLegacyTagIndexes(raw(t, append(index, bson.E{Key: "expireAfterSeconds", Value: int32(1)}))) == nil {
		t.Fatal("TTL index admitted")
	}
	if validateLegacyTagIndexes(append(raw(t, index), raw(t, index)...)) == nil {
		t.Fatal("extra index admitted")
	}
}
