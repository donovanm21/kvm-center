package main

import (
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// replaceMap used to be DeleteMany followed by InsertMany. If the process died
// between the two — OOM kill, power loss, container stop mid-write — the
// collection was empty on disk, and the next start Loaded that empty
// collection, adopted it as truth and persisted it. Hosts and VMs repopulate
// from telemetry; users, roles, api_tokens, idps and hypervisor_certs do not.
// That is a locked-out control plane with no way back short of a backup.
//
// Mongo can't give us a transaction on a standalone deployment, so the write
// is instead arranged so no window of absence exists. These tests pin the two
// properties that guarantee that, neither of which needs a live database:
//
//  1. every kept document is upserted BEFORE anything is deleted, and
//  2. the single delete can only match documents that are genuinely gone.
//
// Get either wrong and the failure is silent data loss, so they're asserted
// structurally rather than by observing a database that happens to behave.

// idOf pulls the _id out of a ReplaceOne filter.
func idOf(t *testing.T, m mongo.WriteModel) string {
	t.Helper()
	r, ok := m.(*mongo.ReplaceOneModel)
	if !ok {
		t.Fatalf("expected *mongo.ReplaceOneModel, got %T", m)
	}
	d, ok := r.Filter.(bson.D)
	if !ok {
		t.Fatalf("filter is %T, want bson.D", r.Filter)
	}
	for _, e := range d {
		if e.Key == "_id" {
			s, _ := e.Value.(string)
			return s
		}
	}
	t.Fatal("filter has no _id")
	return ""
}

// ninOf pulls the $nin id list out of the trailing DeleteMany model.
func ninOf(t *testing.T, m mongo.WriteModel) []string {
	t.Helper()
	dm, ok := m.(*mongo.DeleteManyModel)
	if !ok {
		t.Fatalf("expected *mongo.DeleteManyModel, got %T", m)
	}
	d, ok := dm.Filter.(bson.D)
	if !ok {
		t.Fatalf("delete filter is %T, want bson.D", dm.Filter)
	}
	for _, e := range d {
		if e.Key != "_id" {
			continue
		}
		inner, ok := e.Value.(bson.D)
		if !ok {
			t.Fatalf("_id clause is %T, want bson.D", e.Value)
		}
		for _, ie := range inner {
			if ie.Key != "$nin" {
				t.Fatalf("delete uses %q, want $nin — any other operator risks "+
					"deleting documents that are being kept", ie.Key)
			}
			arr, ok := ie.Value.(bson.A)
			if !ok {
				t.Fatalf("$nin value is %T, want bson.A", ie.Value)
			}
			out := make([]string, 0, len(arr))
			for _, v := range arr {
				s, _ := v.(string)
				out = append(out, s)
			}
			return out
		}
	}
	t.Fatal("delete filter has no _id $nin clause")
	return nil
}

type replaceDoc struct {
	ID   string `bson:"_id"`
	Name string `bson:"name"`
}

func TestBuildReplaceModelsKeepsEveryDocumentPresent(t *testing.T) {
	docs := []any{
		replaceDoc{ID: "a", Name: "alpha"},
		replaceDoc{ID: "b", Name: "bravo"},
		replaceDoc{ID: "c", Name: "charlie"},
	}

	models, err := buildReplaceModels("users", docs)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(models) != len(docs)+1 {
		t.Fatalf("got %d models, want %d upserts + 1 delete", len(models), len(docs))
	}

	// The delete must be LAST. If it sorted anywhere else, an ordered bulk
	// write would remove documents before their replacements landed, which is
	// the very gap this design removes.
	for i, m := range models[:len(models)-1] {
		if _, isDelete := m.(*mongo.DeleteManyModel); isDelete {
			t.Fatalf("delete model at index %d, must be last", i)
		}
	}
	if _, ok := models[len(models)-1].(*mongo.DeleteManyModel); !ok {
		t.Fatalf("last model is %T, want *mongo.DeleteManyModel", models[len(models)-1])
	}

	// Every upsert must actually upsert — a plain replace would silently drop
	// documents that don't exist yet, e.g. on a freshly restored database.
	for i, m := range models[:len(models)-1] {
		r := m.(*mongo.ReplaceOneModel)
		if r.Upsert == nil || !*r.Upsert {
			t.Errorf("model %d (_id=%s) does not set upsert", i, idOf(t, m))
		}
	}

	// The $nin must list every id being kept. Anything missing here would be
	// deleted by the trailing sweep.
	kept := map[string]bool{}
	for _, m := range models[:len(models)-1] {
		kept[idOf(t, m)] = true
	}
	nin := ninOf(t, models[len(models)-1])
	for _, id := range nin {
		delete(kept, id)
	}
	if len(kept) != 0 {
		t.Errorf("ids upserted but absent from the delete's $nin: %v — the trailing "+
			"delete would remove documents this save is keeping", kept)
	}
	if len(nin) != len(docs) {
		t.Errorf("$nin has %d ids, want %d (one per kept document)", len(nin), len(docs))
	}
}

// TestBuildReplaceModelsRejectsNonStringID guards the $nin construction. A
// non-string _id would be appended as some other type, the $nin would fail to
// match the stored ids, and the trailing delete would remove the entire
// collection — the exact catastrophe this rewrite exists to prevent. Fail loudly.
func TestBuildReplaceModelsRejectsNonStringID(t *testing.T) {
	bad := []any{
		struct {
			ID   int    `bson:"_id"`
			Name string `bson:"name"`
		}{42, "numeric id"},
	}
	if _, err := buildReplaceModels("users", bad); err == nil {
		t.Fatal("non-string _id accepted; the $nin would match nothing and the " +
			"trailing delete would empty the collection")
	}

	missing := []any{struct {
		Name string `bson:"name"`
	}{"no id at all"}}
	if _, err := buildReplaceModels("users", missing); err == nil {
		t.Fatal("document without _id accepted")
	}
}

// TestBuildReplaceModelsPreservesDocumentBytes confirms the replacement is the
// marshalled document itself rather than a bson.M round-trip, so stored values
// keep the types InsertMany would have written.
func TestBuildReplaceModelsPreservesDocumentBytes(t *testing.T) {
	doc := replaceDoc{ID: "a", Name: "alpha"}
	models, err := buildReplaceModels("users", []any{doc})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	r := models[0].(*mongo.ReplaceOneModel)
	raw, ok := r.Replacement.(bson.Raw)
	if !ok {
		t.Fatalf("replacement is %T, want bson.Raw", r.Replacement)
	}
	want, err := bson.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != string(want) {
		t.Error("replacement bytes differ from the marshalled document")
	}
}

func TestObsoleteUsernameOwnersExcludesEveryRetainedID(t *testing.T) {
	plan, err := buildReplaceModels(mcoUsers, []any{
		bson.M{"_id": "retained", "username": "existing"},
		bson.M{"_id": "recreated", "username": "reused"},
	})
	if err != nil {
		t.Fatal(err)
	}
	filter := obsoleteUsernameOwners(plan)
	if len(filter) != 2 || filter[0].Key != "_id" || filter[1].Key != "username" {
		t.Fatalf("cleanup must constrain both identity and username: %v", filter)
	}
	excluded := filter[0].Value.(bson.D)
	names := filter[1].Value.(bson.D)
	ids := excluded[0].Value.(bson.A)
	usernames := names[0].Value.(bson.A)
	if excluded[0].Key != "$nin" || len(ids) != 2 || ids[0] != "retained" || ids[1] != "recreated" {
		t.Fatalf("cleanup can select a retained ID: %v", excluded)
	}
	if names[0].Key != "$in" || len(usernames) != 2 || usernames[0] != "existing" || usernames[1] != "reused" {
		t.Fatalf("cleanup can select an unrelated username: %v", names)
	}
	plan, err = buildReplaceModels("fixture", []any{bson.M{"_id": "no-username"}})
	if err != nil {
		t.Fatal(err)
	}
	if obsoleteUsernameOwners(plan) != nil {
		t.Fatal("cleanup planned without a username")
	}
}
