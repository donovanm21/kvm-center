package offlinestaging

import (
	"encoding/json"
	"errors"
	"go.mongodb.org/mongo-driver/bson"
	"regexp"
)

const LegacyTagSource = "c018b31b74d7d75ed8c49921dc0812bf5999d81f"

// LegacyTagState is the existing current assignment format. Original BSON and
// index/provenance bytes remain in the separately retained immutable bundle.
type LegacyTagState struct {
	Version int                 `json:"version"`
	ByVM    map[string][]string `json:"by_vm"`
}

var legacyTagUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var legacyTagName = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,30}[a-z0-9])?$`)

func decodeLegacyTags(raw bson.Raw) (string, []string, error) {
	if err := validateRaw(raw, 0, false); err != nil {
		return "", nil, err
	}
	fields, err := raw.Elements()
	if err != nil || len(fields) != 2 {
		return "", nil, errors.New("unsupported legacy tag fields")
	}
	for _, f := range fields {
		if f.Key() != "_id" && f.Key() != "tags" {
			return "", nil, errors.New("unsupported legacy tag field/alias")
		}
	}
	id, ok := raw.Lookup("_id").StringValueOK()
	if !ok || !legacyTagUUID.MatchString(id) {
		return "", nil, errors.New("noncanonical legacy tag UUID")
	}
	arr, ok := raw.Lookup("tags").ArrayOK()
	if !ok {
		return "", nil, errors.New("legacy tags must be an array")
	}
	values, err := arr.Values()
	if err != nil || len(values) == 0 || len(values) > 24 {
		return "", nil, errors.New("unsupported legacy tag count")
	}
	tags := make([]string, 0, len(values))
	previous := ""
	for _, value := range values {
		tag, ok := value.StringValueOK()
		if !ok || !legacyTagName.MatchString(tag) || tag <= previous {
			return "", nil, errors.New("legacy tags must be canonical, sorted and unique")
		}
		tags = append(tags, tag)
		previous = tag
	}
	return id, tags, nil
}
func validateLegacyTagIndexes(raw []byte) error {
	docs, err := documents(raw)
	if err != nil || len(docs) != 1 {
		return errors.New("legacy tags require only the identity index")
	}
	d := docs[0]
	fields, err := d.Elements()
	if err != nil {
		return err
	}
	for _, f := range fields {
		switch f.Key() {
		case "v", "name", "key":
		case "ns":
			if _, ok := f.Value().StringValueOK(); !ok {
				return errors.New("invalid legacy tag index namespace")
			}
		default:
			return errors.New("unsupported legacy tag index options")
		}
	}
	v, ok := d.Lookup("v").Int32OK()
	if !ok || v != 2 {
		return errors.New("unsupported legacy tag index version")
	}
	name, ok := d.Lookup("name").StringValueOK()
	if !ok || name != "_id_" {
		return errors.New("unsupported legacy tag index name")
	}
	key, ok := d.Lookup("key").DocumentOK()
	if !ok {
		return errors.New("unsupported legacy tag index key")
	}
	els, err := key.Elements()
	if err != nil || len(els) != 1 {
		return errors.New("unsupported legacy tag index keys")
	}
	dir, ok := key.Lookup("_id").Int32OK()
	if !ok || dir != 1 {
		return errors.New("unsupported legacy tag index direction")
	}
	return nil
}
func ExportLegacyTagState(path string) (*LegacyTagState, error) {
	_, s, err := verifySnapshot(path)
	if err != nil {
		return nil, err
	}
	if s.manifest.SourceBuild != LegacyTagSource {
		return nil, errors.New("legacy tags require the pinned saved-source contract")
	}
	current, err := documents(s.files["vm_tag_state.bson"])
	if err != nil || len(current) != 0 {
		return nil, errors.New("mixed legacy/current tag state refused")
	}
	if err := validateLegacyTagIndexes(s.files["vm_tags.indexes.bson"]); err != nil {
		return nil, err
	}
	docs, err := documents(s.files["vm_tags.bson"])
	if err != nil || len(docs) == 0 || len(docs) > 4096 {
		return nil, errors.New("unsupported legacy tag record count")
	}
	state := &LegacyTagState{Version: 1, ByVM: map[string][]string{}}
	for _, doc := range docs {
		id, tags, err := decodeLegacyTags(doc)
		if err != nil {
			return nil, err
		}
		if _, ok := state.ByVM[id]; ok {
			return nil, errors.New("duplicate legacy tag identity")
		}
		state.ByVM[id] = tags
	}
	encoded, err := json.Marshal(state)
	if err != nil || len(encoded) > 1<<20 {
		return nil, errors.New("legacy tag state exceeds current encoded limit")
	}
	return state, nil
}
