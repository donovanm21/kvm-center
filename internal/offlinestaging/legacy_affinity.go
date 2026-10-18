package offlinestaging

import (
	"errors"
	"kvm-center/internal/affinitypolicy"
)

func ExportLegacyAffinityState(path string) (*affinitypolicy.State, error) {
	_, s, err := verifySnapshot(path)
	if err != nil {
		return nil, err
	}
	if s.manifest.SourceBuild != affinitypolicy.Source {
		return nil, errors.New("affinity requires pinned deployed-source contract")
	}
	current, err := documents(s.files["affinity_state.bson"])
	if err != nil || len(current) != 0 {
		return nil, errors.New("mixed legacy/current affinity state refused")
	}
	if err := validateLegacyTagIndexes(s.files["affinity_rules.indexes.bson"]); err != nil {
		return nil, errors.New("unsupported affinity index semantics")
	}
	docs, err := documents(s.files["affinity_rules.bson"])
	if err != nil {
		return nil, err
	}
	state := &affinitypolicy.State{Version: 1, SourceContract: affinitypolicy.Source, Originals: map[string][]byte{}}
	for _, raw := range docs {
		r, err := affinitypolicy.Decode(raw)
		if err != nil {
			return nil, err
		}
		if state.Originals[r.ID] != nil {
			return nil, errors.New("duplicate affinity rule identity")
		}
		state.Originals[r.ID] = append([]byte(nil), raw...)
	}
	if err := state.Validate(); err != nil {
		return nil, err
	}
	return state, nil
}
